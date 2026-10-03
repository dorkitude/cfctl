# Changelog

All notable user-facing changes to `cfctl` are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

cfctl counts versions as `0.MINOR.NNN` (`0.2.001`, `0.2.002`, ...), bumping
`NNN` at each meaningful milestone. That isn't valid semver, so a release of
`0.2.NNN` is tagged `v0.2.N` (e.g. `0.2.007` → `v0.2.7`). See
`docs/ARCHITECTURE.md` for how to bump.

## [Unreleased]

## [0.2.004] - 2026-10-03

Hand-written, wrangler-shaped command groups on top of the generated API tree.

### Workers

#### Added (Workers core, area A)

- `cfctl workers` group: `list`, `get`, `delete`, `deploy`, `versions`,
  `deployments`, `rollback`, `secret`, `triggers`, `crons`, `routes`,
  `domains`, `subdomain`, `dev-url`, `tail`, `logs`, `dispatch-namespace`,
  `preview`. Top-level shortcuts like wrangler's: `cfctl deploy`, `tail`,
  `rollback`, `secret`, `versions`, `deployments`, `triggers`,
  `dispatch-namespace`, `preview`.
- `deploy` / `versions upload`: upload a pre-built ES module or service-worker
  script with metadata and bindings, from flags (`--var`, `--kv`, `--r2`,
  `--d1`, `--service`, `--queue`, `--ai`, `--binding JSON`, `--bindings-file`)
  or a `wrangler.toml` / `wrangler.json` / `wrangler.jsonc` (`--config`,
  `--env`). Static assets go through the assets upload-session API
  (`_headers`, `_redirects`, `.assetsignore` honored). Durable Object
  migrations are applied from the script's current tag. Relative imports of an
  unbundled entry point are uploaded as modules; `--bundle` runs esbuild if it
  is on PATH. `--dry-run` prints the metadata without sending anything.
  Existing secrets are always kept (`keep_bindings`); `--keep-vars` keeps vars.
- `deploy` / `triggers deploy` apply cron triggers, routes (zone inferred from
  the hostname), custom domains, and the workers.dev setting.
- `versions list|view|upload|deploy`: version prefixes and `latest` resolve to
  IDs; `versions deploy a@10 b` splits traffic 10/90.
- `versions secret put|delete|bulk|list`: change secrets in a new version
  without deploying it (Workers Versions API, other bindings inherited).
- `secret put|list|delete|bulk`: values come from a hidden prompt or stdin and
  are never printed; `bulk` takes JSON (`null` deletes) or `.env` input.
- `tail`: creates a tail, streams it over a WebSocket (`--format pretty|json`,
  `--status`, `--method`, `--header`, `--ip`, `--search`, `--sampling-rate`),
  and deletes the tail on exit or Ctrl-C.
- `workers logs`: query Workers Logs (observability telemetry query API).
- `dispatch-namespace list|get|create|rename|delete|scripts`.
- `workers preview deploy|list|get|delete|deployments`, `preview secret
  put|delete|list|bulk`, `preview base-config secret ...` (Worker Previews,
  open beta).
- Destructive commands (`workers delete`, `rollback`, `secret delete`,
  deletions in `secret bulk`, `routes delete`, `domains delete`,
  `crons clear`, `subdomain set`, `dispatch-namespace delete`,
  `preview delete`) confirm on a terminal and need `--yes` otherwise.

#### Changed

- `github.com/pelletier/go-toml/v2` is now a direct dependency (it was already
  in the module graph via viper) for reading `wrangler.toml`.

#### Docs

- `docs/commands/workers.md`: command reference for the Workers commands.
- `docs/wrangler-map/workers.tsv`: every wrangler Workers-core command and its
  cfctl equivalent.

## [0.2.003] - 2026-10-03

### Changed

- Query parameters with characters flags can't hold get kebab-case flags
  (`subject~neq` → `--subject-neq`); only templated names
  (`meta.<field>[<operator>]`) still need `--query`.
- Typos in `cfctl api <tag> <op>` get "did you mean" suggestions instead of
  the full group help.

## [0.2.002] - 2026-10-03

### Fixed

- `api describe` merges properties defined by several `allOf` members (e.g. the
  Worker upload `metadata` part now shows bindings, assets, and the rest).
- `api describe` examples stored in the spec as JSON-encoded strings are shown
  as JSON.

### Docs

- `docs/ARCHITECTURE.md`: package layout, request layer, read-only guard,
  generated commands, conventions for hand-written command groups, versioning.
- README: `cfctl api`, `cfctl graphql`, read-only mode, new global flags.
- AGENTS.md / CONTRIBUTING.md: versioning (`0.2.NNN` → tag `v0.2.N`), the
  shared HTTP client rule, `make spec` / `make generate`.

## [0.2.001] - 2026-10-03

### Added

- `cfctl api request <METHOD> <PATH>`: raw requests to any Cloudflare API v4
  path, with `{account_id}` / `{zone_id}` placeholders (`--zone` takes a zone
  name or ID), `--data JSON|@file|-`, `--form k=v|k=@file` (multipart),
  `--query`, `--header`, `--all` (page and cursor pagination), and `--raw`.
- `cfctl api <tag> <operation>`: a generated command for every operation in
  Cloudflare's OpenAPI spec (3,645 operations in 575 groups), with path
  params as arguments, query params as flags, and `--data` / `--form` bodies.
- Discovery: `cfctl api tags`, `api list [--tag] [--method]`,
  `api search <words>`, `api describe <tag> <op>` (params, request body
  schema, example), `api spec-info`, `api spec`.
- `cfctl graphql`: raw GraphQL Analytics queries (same UX as `linctl graphql`),
  with `{account_id}` / `{zone_id}` substitution.
- `--read-only` / `CFCTL_READONLY=1`: refuse every request except GET, HEAD,
  OPTIONS, and GraphQL queries, before it is sent. Enforced in one HTTP
  transport shared by every client, including the cloudflare-go SDK.
- `--timeout` (per request attempt, default 30s).
- `make spec` / `scripts/update-spec.sh` to refresh the vendored spec.

### Changed

- All API traffic (SDK and raw) shares one transport: retries on 429 and 5xx
  with backoff (5xx only for idempotent requests), per-attempt timeouts, and
  `--debug` logging (method, path, status, duration only).
- `cfctl --version` prints the internal version (`0.2.001`) instead of `dev`.

