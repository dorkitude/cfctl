# cfctl architecture

This is the map for anyone (human or agent) extending cfctl. The goal of the
project: cfctl covers everything `wrangler` does and every operation in the
Cloudflare API, with ergonomic hand-written commands for everyday work and
generated commands for the long tail.

## Package layout

```text
main.go                    entry point; passes an -ldflags version to cmd.SetVersion
cmd/                       Cobra commands (package cmd)
  root.go                  root command, global flags, PersistentPreRunE (applies --debug/--read-only/--timeout)
  auth.go whoami.go        hand-written: auth, identity
  domains.go autorenew.go  hand-written: Registrar
  zones.go records.go      hand-written: zones, DNS records (cloudflare-go SDK)
  api.go                   `cfctl api` + request/list/search/tags/describe/spec-info/spec
  api_gen.go               generated `cfctl api <tag> <op>` commands (built from the ops table)
  api_exec.go              shared plumbing for raw + generated: session, placeholders, bodies, output, confirm
  graphql.go               `cfctl graphql`
  fake_test.go             fake Cloudflare API (httptest) + runCLI harness
  *_test.go                in-process CLI tests
internal/api/              the request layer (no Cobra, no config)
  transport.go             read-only guard + debug transports, settings (SetReadOnly, SetDebug, Timeout)
  retry.go                 retries (429/5xx) + per-attempt timeout transport
  client.go                raw client: Request/Response, envelope decoding, Error, GraphQL
  paginate.go              All(): page and cursor pagination, Merge()
  body.go                  --data / --form parsing, multipart encoding
internal/apispec/          the embedded Cloudflare OpenAPI spec
  openapi.json.gz          vendored spec (minified + gzipped), //go:embed
  spec-source.json         upstream repo/commit/date of the vendored spec
  ops_gen.go               GENERATED operations table (do not edit)
  apispec.go               Op/Param types, lookups (ByID, Find, Tags, Search)
  describe.go              schema rendering for `api describe` ($ref-resolving, depth/size-limited, cycle-safe)
  gen/main.go              the generator (`go generate ./...`) and spec importer
internal/client/           cloudflare-go SDK construction, token verification, zone resolution, SDK error formatting
internal/config/           Viper config + token file (~/.config/cfctl)
internal/output/ ui/       JSON output helper; lipgloss styles
internal/version/          Version = "0.2.710" (semver, large patch numbers)
scripts/update-spec.sh     `make spec`: refresh the vendored spec
```

## The request layer

Every HTTP request cfctl makes goes through one `http.Client` built by
`api.NewHTTPClient()`. The cloudflare-go SDK gets it via
`option.WithHTTPClient` (in `internal/client.NewClient`), and the raw client
(`api.New`) uses it directly. Its transport chain:

```text
readOnlyTransport → retryTransport → debugTransport → http.DefaultTransport
```

- **Read-only guard** (`readOnlyTransport`). On when `--read-only` is passed
  or `CFCTL_READONLY` is truthy (checked on every request, so the env var
  can't be bypassed by code that forgets the flag). Allowed: `GET`, `HEAD`,
  `OPTIONS`, and `POST /client/v4/graphql`. Anything else fails *before
  sending* with `refusing <METHOD> <path>: read-only mode`
  (`*api.ReadOnlyError`, `errors.Is(err, api.ErrReadOnly)`). With `--debug` it
  logs `-> refused, not sent`. Because the guard is a transport, no command
  can opt out. Never add a second HTTP client that skips `NewHTTPClient`.
- **Retries** (`retryTransport`). 429 is always retried (the request wasn't
  processed). 5xx is retried only for GET/HEAD/OPTIONS/PUT/DELETE and GraphQL
  queries. Backoff 0.5s, 1s, 2s (cap 8s), honoring `Retry-After` up to 30s;
  `MaxRetries = 3`. Requests whose body can't be replayed aren't retried. The
  SDK's own retries are turned off (`option.WithMaxRetries(0)`), so there is
  one retry policy.
- **Timeouts.** Each attempt is bounded by `api.Timeout` (`--timeout`, default
  30s), including reading the body. Whole commands aren't, so `--all` over
  many pages isn't cut off.
- **Debug** (`--debug`, `CFCTL_DEBUG=1`): `debug: METHOD /path -> status
  (duration)` per attempt. Never headers, query strings, or bodies, so the
  token can't leak.

### Raw client (`internal/api`)

```go
c := api.New(token, config.APIBaseURL())           // "" → https://api.cloudflare.com/client/v4
resp, err := c.Do(ctx, api.Request{Method: "GET", Path: "/zones", Query: q})
resp.Envelope.Result                                // json.RawMessage
res, err := c.All(ctx, api.Request{Path: "/zones"}, 0) // every page, merged
body, err := c.GraphQL(ctx, query, vars)
```

- `Do` decodes Cloudflare's envelope (`success`, `errors`, `messages`,
  `result`, `result_info`) into `resp.Envelope` when the body is one;
  non-JSON bodies (KV values, BIND exports) are left in `resp.Body`. Non-2xx
  or `success:false` returns `*api.Error` (`HTTP 403: message (code N)`),
  with the response attached for `--raw`.
- `All` detects the style from the first page's `result_info`: a `cursor` (or
  `cursors.after`) → repeats with `?cursor=`; `total_pages` → `page=2..N`;
  otherwise one page. It stops on an empty page, a repeated cursor, or after
  `maxPages` requests (default 1000), so loops are always bounded. `Merge`
  concatenates arrays, and the array fields of object results (R2's
  `{"buckets": [...]}`).
- Absolute URLs are only allowed for the API host (so the token is never sent
  elsewhere).

## Raw escape hatches

- `cfctl api request <METHOD> <PATH>`: `cmd/api.go`. Placeholders are filled
  by `apiSession.fillPath` (`cmd/api_exec.go`): `{account_id}` from
  `--account` / `CFCTL_ACCOUNT_ID` / config (or the only account the token
  sees), `{zone_id}` / `{zone_identifier}` from `--zone` (name → ID via
  `GET /zones?name=`; a 32-hex value is used as-is) or `CFCTL_ZONE`. Values
  are path-escaped; unknown placeholders are an error.
- `cfctl graphql`: `cmd/graphql.go`, the same UX as `linctl graphql` (positional
  query, `--query`, `--file`, or stdin; `--variables` / `--variables-file`),
  plus `{account_id}` / `{zone_id}` substitution in the query and in string
  variables.

## Generated `api` commands

1. `internal/apispec/openapi.json.gz` is Cloudflare's spec
   (github.com/cloudflare/api-schemas), vendored by `make spec`.
2. `go generate ./...` runs `internal/apispec/gen`, which writes
   `ops_gen.go`: one `apispec.Op` per operation (operationId, method, path,
   tag, slugs, summary, first description line, params with type/enum/
   required/default, body content types, deprecated, confirm).
3. At startup `cmd/api_gen.go` creates one command per tag
   (`cfctl api <tag-slug>`). The operation commands under a tag are built only
   when an invocation names that tag (`prepareCommands`), because building all
   ~3,600 costs ~60 ms. Tests call `populateAllTags()`.

Mapping rules (`newOpCommand`, `runOp`):

- Command: `cfctl api <tag-slug> <op-slug> [path params...]`.
- Tag slug: kebab-case of the first OpenAPI tag; tags differing only in case or
  punctuation share a group. Op slug: kebab-case of the summary without
  parentheticals; colliding ops (only those) fall back to the full summary,
  then to the operationId, then `-2`, `-3`. Slugs only change if an op's own
  summary changes or it starts colliding.
- `{account_id}` and `{zone_id}` / `{zone_identifier}` come from config and
  `--zone`; every other path param is a positional argument, in path order.
- Query params are flags: `per_page` → `--per-page`. Names clashing with
  cfctl's flags get `--param-<name>` (e.g. `--param-query`); names that can't
  be flags (`meta.<field>[<operator>]`) are listed in help and set with
  `--query k=v`. Required params are enforced; enums are validated
  (`--query` bypasses both). Booleans accept bare `--flag`.
- Bodies: `--data JSON|@file|-` (sent with the op's first content type, or
  `--content-type`), `--form` for multipart. A required body is enforced.
- Header params: `--header 'Name: value'` (required ones enforced).
- `DELETE` ops and ops the spec marks `x-forge-require-confirmation` ask for
  confirmation, or need `--yes` when stdin isn't a terminal. In read-only mode
  they don't ask; the guard refuses them.
- Output and pagination are the same as `api request` (`execRequest`).

`TestCoverage` (cmd/api_test.go) asserts every operation in the embedded spec
is reachable through exactly one generated command and that `describe` works
for all of them; `TestDescribeAll` checks rendering has no runaway output.

### Generated vs hand-written

Generated commands are complete but literal: JSON in, JSON out, API names.
Hand-written groups (`zones`, `records`, `domains`, ...) are the ergonomic
layer: friendly names, tables, flags for common fields, confirmations. They
coexist; the generated tree is the fallback and the reference. When you add a
hand-written group for an area, keep the generated commands (don't hide them)
and mention the generated equivalent in the hand-written command's help when
useful.

## Adding a hand-written command group

Conventions (see `cmd/records.go` and `cmd/zones.go` for examples):

1. One file per group in `cmd/` (`cmd/r2.go`), a parent command
   (`r2Cmd`) plus subcommands, registered in `init()`. Add its name to the right area in `rootGroups`
   (`cmd/root.go`) so `cfctl --help` lists it under Core / Workers / Storage /
   Platform / Zone & account / API & raw.
2. Talk to the API through either the SDK (`getApp(ctx)` → `app.Client.R2...`)
   or the raw client (`newAPISession(cmd)` → `s.c.Do/All`). Both go through the
   guard. Prefer the raw client where the SDK is awkward; never build another
   `http.Client`.
3. Resolve zones with `client.ResolveZone` (SDK) or `apiSession.zone` (raw):
   accept a zone name or ID everywhere.
4. Output: styled text by default (`ui.TitleStyle` header, aligned rows, `ui.Success`
   / `ui.Warn`), `--json` via `printJSON(v)` returning the API objects. Errors via
   `apiErr("failed to ...", err)` (SDK) or the `*api.Error` as-is (raw); never
   include request headers.
5. Destructive commands: require confirmation; accept `--yes` / `-y` to skip
   it; refuse without `--yes` when stdin isn't a terminal (see `confirm` in
   `cmd/api_exec.go`). Don't prompt in read-only mode.
6. Pagination: list everything by default with a bounded loop
   (`s.c.All(ctx, req, maxPages)` or the SDK's auto-pager), and offer
   `--page`/`--per-page` where the API supports it.
7. Tests: add `cmd/<group>_test.go` using the fake API. For new routes, set
   `f.custom` (see `apiFake` in `cmd/api_test.go`) or add routes to
   `fakeCF.serve`. Run commands with `runCLI(t, stdin, args...)`, assert on
   output, on `f.Requests()` (method, path, query, body, content type), and
   call `assertNoToken`. Test the read-only path for mutating commands.
   Never call the real API from tests.
8. Docs: add the commands to the area's `docs/commands/*.md` page (examples
   must exist: `make docs-check`), and to `docs/wrangler-map/*.tsv` (then
   `make wrangler-doc`) if they mirror a wrangler command. New `.md`
   files under `docs/` are embedded automatically and become `cfctl docs`
   topics named after the file.

## Versioning and the changelog

- The version lives in `internal/version/version.go` as semver with large
  patch numbers (`0.2.710`, `0.2.711`, ...). `cfctl --version` prints it (an `-ldflags`
  value passed to `main.version` overrides it for release builds).
- Bump the patch at each meaningful milestone commit and add a
  `## [X.Y.Z] - YYYY-MM-DD` section to `CHANGELOG.md` (Keep a Changelog:
  Added / Changed / Fixed / Docs). Put in-progress notes under
  `## [Unreleased]`.
- Releases are tagged with the version (`v0.2.710`); the tag push runs
  GoReleaser. Only the maintainer cuts releases.

## Refreshing the API spec

```bash
make spec               # clone cloudflare/api-schemas, vendor, regenerate, run coverage tests
make generate           # regenerate ops_gen.go only (after changing gen/main.go)
```

Review the `ops_gen.go` diff for renamed slugs before committing; a renamed
slug breaks scripts that call it.
