# Live smoke tests

Two read-only checks run cfctl against a **real** Cloudflare account (the one
in `~/.config/cfctl`). Both force read-only mode, so nothing in the account
can change: every request other than GET/HEAD/OPTIONS and GraphQL queries is
refused before it's sent (see the read-only guard in `docs/ARCHITECTURE.md`).

Neither runs in CI or `go test`; they need a token. Run them before a release
or after a change to a command group, and expect a few minutes each: they are
throttled to stay well under Cloudflare's rate limit (1,200 requests / 5 min).

## 1. Hand-written read commands: `make smoke`

```bash
make smoke                                   # or: CFCTL_READONLY=1 scripts/smoke.sh
SMOKE_ZONE=example.com make smoke            # pick the zone (default: the one with the most DNS records)
SMOKE_FILTER='^r2 ' scripts/smoke.sh         # only commands whose label matches
SMOKE_VERBOSE=1 SMOKE_KEEP=/tmp/smoke scripts/smoke.sh   # print errors; keep every stdout/stderr
```

`scripts/smoke.sh`:

1. Exports `CFCTL_READONLY=1` and proves the guard is on by sending a DELETE
   to a closed local port (it must be refused; the script stops otherwise).
2. Discovers arguments: the zone, its first DNS record and setting, the first
   bucket that has an object, the first Worker and version, token, member,
   role, ruleset, notification policy, AI model, ... Where the account has no
   such resource (no KV namespaces, say), it uses a placeholder
   (`cfctl-smoke-nonexistent` or 32 zeros) and expects a friendly "not found".
3. Runs every hand-written read command (list/get/info/stat/ls/du/usage/
   status/settings/analytics/...) twice: text mode and `--json`.
4. Classifies each run:
   - **PASS**: exit 0; text mode printed something (and no Go formatting like
     `<nil>`, `map[`, `%!`); `--json` printed valid JSON that isn't `null`.
   - **EXPECTED-UNAVAILABLE**: non-zero exit with a friendly error: HTTP
     400/401/403/404 (product not enabled, plan, token type, missing
     resource), or a documented read-only refusal (`containers images list`
     needs a POST for a registry credential).
   - **FAIL**: a panic, a timeout, a non-zero exit with no message, raw JSON in
     the error, an HTTP 5xx, a read refused by the guard, or bad `--json`.
5. Prints a table (command, mode, exit, outcome, detail) and a summary line,
   and exits non-zero if anything FAILed. `SMOKE_RESULTS=file.tsv` saves the
   per-run table.

Mutating commands (create/delete/put/deploy/...) and commands that only make
sense with local files or a terminal (`dev`, `tail`, `deploy`) aren't run.

## 2. Every generated GET: `cfctl api smoke`

```bash
make smoke-sweep SMOKE_ZONE=example.com          # build, then sweep (writes generated-get-sweep.tsv)
go build -o cfctl . && CFCTL_READONLY=1 ./cfctl api smoke --zone example.com
./cfctl api smoke --dry-run                      # the plan: commands and skips, no API calls
./cfctl api smoke --zone example.com --filter '^/zones/\{zone_id\}/ssl'   # a subset (regexp on id, tag, path)
```

`cfctl api smoke` is a hidden maintainer command. It selects every GET
operation in the embedded spec whose only path parameters are `{account_id}`
and/or `{zone_id}` (plus account aliases such as `{account_identifier}`, and
operations with no path parameters at all: `/user/...`, `/radar/...`,
`/zones`, ...). Account-level operations run once; zone-level ones against
`--zone` (pass a zone *name*: it is also used for `host`/`url` parameters).

Each operation runs through the real CLI, as a child process
`cfctl api <tag> <op> [--required-flag=value] [--zone Z] --debug`, so flag
mapping, path filling, output, and exit codes are all exercised. Required
query parameters are filled from the spec's enum (first value) or default, or
from well-known names (`since`/`until`/`from`/`to` → the last 24 hours,
`zone_id`, `account_tag`, `host`, `url`, `ip`, `model`, ...); Radar
operations also get `dateRange=1d`, which Radar requires although the spec
marks it optional. Operations
whose required parameters can't be satisfied are **skipped** and reported.
Paginated operations that succeed are run again with `--all --max-pages 3`.

Flags: `--out` (TSV path, default `docs/smoke/generated-get-sweep.tsv`),
`--rate` (operations per second, default 2.5), `--filter`, `--limit`,
`--check-all=false`, `--dry-run`. It exits non-zero on any client-side bug.

### Reading `generated-get-sweep.tsv`

| column | meaning |
| --- | --- |
| `operationId` | the spec's operationId (`cfctl api describe <tag> <op>` shows it) |
| `tag` | the OpenAPI tag (the `cfctl api <tag-slug>` group) |
| `path` | the templated path |
| `status` | HTTP status of the operation's own request after retries (`-` if none was sent) |
| `outcome` | see below |
| `all_check` | result of the `--all --max-pages 3` re-run (`-` when not paginated or not 2xx) |
| `detail` | the error message, the skip reason, or what went wrong |

Outcomes:

- `ok`: 2xx, exit 0, output well formed (JSON parses; non-JSON bodies such as
  BIND exports and text are passed through).
- `expected-4xx`: a 4xx with a friendly error. Mostly products that aren't
  enabled on the account, plan limits, user-level endpoints called with an
  account token, or endpoints that need setup first.
- `rate-limited`: 429 even after cfctl's retries.
- `5xx`: Cloudflare server error after retries. Re-run the operation alone
  before calling it a cfctl bug.
- `client-bug`: cfctl's fault: a panic, a timeout, a 2xx with a non-zero exit,
  output that looks like JSON but doesn't parse, raw JSON in an error, a GET
  refused by the guard, or a failure before sending (bad flag mapping).
- `skipped`: a required parameter couldn't be satisfied from the spec.

Quick summaries:

```bash
cut -f5 docs/smoke/generated-get-sweep.tsv | sort | uniq -c
awk -F'\t' '$5=="client-bug" || $5=="5xx"' docs/smoke/generated-get-sweep.tsv
awk -F'\t' '$6!="-" && $6!="ok" && NR>1' docs/smoke/generated-get-sweep.tsv   # --all problems
```

The checked-in TSV is a snapshot from one account (few products enabled), so
most `expected-4xx` rows say "not enabled"/"not entitled"; another account
will differ.
