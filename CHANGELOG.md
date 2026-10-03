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

### Storage

#### Added (storage and data)

- `cfctl kv`: namespace list/get/create/rename/delete; key list/get/put/delete with metadata, `--ttl` and `--expiration`; bulk put/get/delete from JSON files (batched to the API limits). Namespaces by title or ID.
- `cfctl r2 buckets`: list/get (with object count and size from GraphQL)/create/update (default storage class)/delete (`--force` empties first), and bucket settings: `cors`, `lifecycle` (add/remove/set), `lock`, `domain`, `dev-url`, `local-uploads`, `notification`, `sippy` (aws/gcs/s3/azure), `jobs` (prefix delete, storage-class migration), `catalog` (R2 Data Catalog).
- `cfctl r2 ls|stat|get|put|rm|du` (plus wrangler-style `r2 object get|put|delete`): folder view and `-r` listing, streaming downloads, uploads with content-type detection, recursive delete with confirmation and `--dry-run`, and `du --by ext|prefix|class --top N`.
- `cfctl r2 usage`: per-bucket objects and size, Class A/B operations this month and last month (GraphQL `r2OperationsAdaptiveGroups`), and an estimated bill (last month, month to date, projected) from one price table (developers.cloudflare.com/r2/pricing, checked 2026-10-03), with free tier and round-up rules.
- `cfctl r2 temp-credentials`: short-lived S3 credentials scoped to a bucket, prefixes, or objects (`--env` for `AWS_*` exports).
- `cfctl r2 sync <dir> <bucket>/<prefix>` (and the reverse): MD5/size comparison, `--delete`, `--exclude`, `--dry-run` (works read-only), parallel transfers over the S3 API with temporary credentials. Large `r2 put` uploads use S3 multipart.
- `cfctl d1`: list/info (with 24h query stats)/create/update/delete, `execute` (tables or `--json`, `--param`, large files via the import API), `export`, `import`, `time-travel info|restore`, `insights` (GraphQL), and wrangler-compatible `migrations create|list|apply` (`d1_migrations` table; prints a Time Travel bookmark before applying).
- New dependency: `github.com/aws/aws-sdk-go-v2` (`service/s3`, `credentials`) for R2's S3 API. The S3 client uses cfctl's shared HTTP client, so the read-only guard and `--debug` apply.
- `cfctl queues`: list/get(info)/create/update/delete, consumer list/get/add/update/remove (+ `consumer http add|remove`, `consumer worker add|remove`), pause-delivery/resume-delivery (keeps other settings), purge (+ `purge status`), metrics, send/send-batch/pull/ack/peek, and event `subscription list|get|create|update|delete`. Queues resolve by name or ID.
- `cfctl hyperdrive`: list/get/create/update/delete/restart with `--connection-string` or `--origin-*` flags (public, Access/Tunnel, and Workers VPC origins), caching and mTLS flags, and `planetscale signature`. Passwords are never printed.
- `cfctl vectorize` (v2): list/get/create/delete/info, insert/upsert (NDJSON, batched 5,000 lines), query, get-vectors, delete-vectors, list-vectors (`--all`), and create-/list-/delete-metadata-index (also `metadata-index create|list|delete`).
- `cfctl secrets-store`: store list/get/create/delete (`--force`), secret list/get/create/update/delete/duplicate, quota. Values come from a no-echo prompt, stdin, `--value-file`, or `--value` and are never printed.
- `cfctl k2 streams`: list/get/create/update/delete/subscriptions.
- `cfctl basin`: pipelines (v1) list/get/create/delete/validate-sql and legacy `update --legacy`, `pipelines streams` and `pipelines sinks` CRUD, and `basin catalog` (enable/disable/delete, credential, maintenance, compaction/snapshot-expiration toggles, namespaces, tables, table maintenance). The catalog tree is shared with `r2 bucket catalog` via `newCatalogCmd`.
- `cfctl artifacts`: namespaces list/get/create/delete; repos list/get/create/delete/fork/import/log/file/blob/commit/tree/tokens/issue-token/revoke-token.
- `cfctl agent-memory`: namespace list/get/create/delete; profile list/summary/delete; memories list/get/delete/remember/recall/ingest; session delete.
- Every create/update in these groups accepts `--data` JSON with flags merged on top; destructive commands confirm (or `--yes`).
- Test: no storage-group command defines a local flag that shadows a global flag (a local `--read-only` would bypass the read-only guard; Artifacts uses `--read-only-repo`).

### Platform

#### Added (platform & apps)

- `cfctl pages`: `project list|get|create|edit|delete|purge-build-cache`,
  `deployment list|get|logs|tail|retry|rollback|delete`, `deploy <dir>` (Direct
  Upload: blake3 hashing, check-missing, bucketed upload, upsert, deployment with
  `_headers`, `_redirects`, `_routes.json`, and a pre-built `_worker.js`),
  `domains list|get|add|retry|delete`, `secret list|put|bulk|delete`,
  `download config` (writes `wrangler.jsonc`).
- `cfctl ai`: `models list|search|get|schema`, `tasks`, `authors`,
  `run <model>` (`--prompt`/`--system`/`--data`/`--file`, `--stream`, binary
  output to `--output`), `finetune list|public|create|delete`, `markdown <files>`.
- `cfctl ai-search`: instances, `search`, `stats`, `jobs`, `items`, `namespace`, `tokens`.
- `cfctl workflows`: `list|describe|delete|trigger`, `instances
  list|describe|pause|resume|terminate|restart|send-event|delete` ("latest"
  accepted), `versions list|get|graph`.
- `cfctl containers`: `list|info|instances|versions|delete`, `images list|delete`
  (managed registry), `registries list|configure|delete|credentials`, `build` /
  `push` (wrap docker; registry password via stdin).
- `cfctl browser`: sessions (`list|get|create|view|close`) and one-shot
  `screenshot|pdf|markdown|content|links`.
- `cfctl flagship`: `apps …`, `flags list|get|create|update|delete|changelog|evaluate|enable|disable|set|rollout|split|rules|pull`.
- `cfctl email routing …` (settings, enable/disable, rules, catch-all,
  addresses, DNS) and `cfctl email sending …` (subdomains, DNS, `send`, `send-raw`).
- `cfctl turnstile widget list|get|create|update|delete|rotate-secret` (secrets
  redacted unless `--reveal`).
- `cfctl tunnel list|info|create|delete|token|config|connections|cleanup|route|vnet|run|quick-start`
  (`token` needs `--reveal`; `run` passes the token to cloudflared via `TUNNEL_TOKEN`).
- `cfctl vpc service list|get|create|update|delete`.
- `cfctl cert` and `cfctl mtls-certificate` (upload, list, get, associations, delete).
- `cfctl docs` (embedded README/docs rendered in the terminal, `--list`,
  `--search`, `--raw`), `cfctl completion bash|zsh|fish|powershell`, `cfctl init`
  (worker / scheduled / assets templates).
- `cfctl dev`, `types`, `setup`, `pages dev`, `pages functions build`,
  `containers ssh`: explain they need wrangler's local runtime.
- Friendlier errors for products not enabled / missing token permissions.
- Docs: `docs/commands/platform.md`, `docs/wrangler-map/platform.tsv`.

### Admin

#### Added (area D: zone and account administration)

- `zones settings list|get|set`, `zones create|delete|pause|unpause|activation-check`.
- `cache purge` (`--url`, `--tag`, `--host`, `--prefix`, `--everything`), `cache settings`, `cache dev-mode`.
- `ssl status|mode|verification|universal`, `ssl packs list|get|order|delete`,
  `ssl origin list|get|create|revoke` (key and CSR generated locally, key written to `--key-out` only),
  `ssl custom list|get|upload|delete`.
- `rulesets list|get|phase|versions|delete`, `rulesets rules add|update|delete` (phase aliases; entrypoints created on demand).
- `redirects list|add|delete` (single redirects, wildcard `--from`), `redirects bulk lists|items|add|rules`.
- `transform list|add|delete` (URL rewrites, request/response headers).
- `waf overview|managed|rate-limits`, `waf custom list|add|delete`; `page-rules list|get|delete`.
- `firewall access-rules list|create|delete`; `lists list|get|create|delete|items|items-add|items-remove|operation`.
- `lb list|get|create|update|delete`, `lb pools ...` (+ `health`), `lb monitors ...`.
- `analytics zone|paths|countries|firewall|workers|r2` (GraphQL presets, `--since`, `--json`).
- `accounts list|get`, `members list|get|remove`, `roles list|get`.
- `tokens list|get|verify|permission-groups|create|delete` (new token values go to `--value-out`, never printed).
- `audit-logs list` (v2 API; `--since`, `--before`, `--actor`, `--action`, `--product`, `--zone`, `--v1`).
- `billing subscriptions|zone|profile|history`; `logpush jobs list|get|create|update|delete`, `logpush fields`.
- `notifications policies ...|destinations|available|history`; `healthchecks`, `waiting-rooms`, `spectrum apps`.
- `access apps|policies|groups|idps list|get`, `access organization`; `user get|invites|memberships`.
- `dns dnssec status|enable|disable`, `dns settings get|set`, `dns export|import`.
- Friendly hints on 403 / plan / not-enabled / user-token-only errors.

#### Changed

- `--all` (on `api request` and generated commands) follows every pagination style in the spec:
  page/per_page and variants (`page_no`, `page_size`, `pageSize`, `perPage`), `cursor`/`cursors.after`/`next_cursor`,
  `page_token`/`next_page_token`, `continuation_token`, `continuationToken`/`nextContinuationToken`, `scan_cursor`,
  offset/limit, and Stream's `before` windows; top-level lists (`{data, paging}`) page too.
- `records delete` asks for confirmation; `--yes` skips it (required without a terminal).

#### Docs

- `docs/commands/admin.md`: reference for the administration commands.

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

