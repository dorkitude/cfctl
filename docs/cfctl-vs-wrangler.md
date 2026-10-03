# cfctl vs wrangler

[`wrangler`](https://developers.cloudflare.com/workers/wrangler/) is
Cloudflare's developer CLI: it builds, runs, and deploys Workers and manages
the storage they bind to. It does not manage Registrar domains, DNS records,
zone settings, SSL, cache, WAF, load balancers, tokens, or audit logs, and its
OAuth login can't be granted Registrar or DNS write scopes.

`cfctl` talks to the Cloudflare REST API v4 with an API token. It covers
**every API-backed wrangler command** (deploy, versions, secrets, KV, R2, D1,
Queues, Pages, AI, ...) *and* the rest of Cloudflare: Registrar, DNS, zone and
account administration, and a generated command for each of the **3,645
operations** in Cloudflare's OpenAPI spec, plus raw JSON and GraphQL.

What cfctl deliberately does not do is the *local* half of wrangler: the
`workerd`/Miniflare dev server, TypeScript type generation, and the built-in
bundler. Keep wrangler for those (see
[When to still use wrangler](#when-to-still-use-wrangler)).

Contents:

- [Quick matrix](#quick-matrix)
- [Per-command summary](#per-command-summary)
- [When to still use wrangler](#when-to-still-use-wrangler)
- [Migrating from wrangler](#migrating-from-wrangler)
- [Full per-command map](#full-per-command-map)

Legend: ✅ supported · 🟡 partial (see notes) · ❌ not supported · n/a not applicable

## Quick matrix

### wrangler's command groups

| Area | wrangler | cfctl | cfctl command | Notes |
|---|:---:|:---:|---|---|
| Deploy a Worker | ✅ | 🟡 | `cfctl deploy` | Pre-built or unbundled scripts, bindings from `wrangler.toml`/`.json(c)` or flags, assets, DO migrations, triggers. Bundling only via `--bundle` + esbuild on `PATH`. |
| Versions & gradual rollouts | ✅ | ✅ | `cfctl versions list\|view\|upload\|deploy` | `versions deploy a@10 b` splits traffic; `upload` has deploy's bundling caveat. |
| Deployments & rollback | ✅ | ✅ | `cfctl deployments`, `cfctl rollback` | |
| Live tail | ✅ | 🟡 | `cfctl tail` | Same server-side filters; no `--ip self`, no auto-reconnect. |
| Stored logs (Workers Logs) | ❌ | ✅ | `cfctl workers logs` | |
| Secrets | ✅ | ✅ | `cfctl secret put\|list\|delete\|bulk` | Values from a hidden prompt or stdin, never argv. |
| Triggers, routes, custom domains | ✅ | ✅ | `cfctl triggers`, `cfctl workers routes\|domains\|crons` | Routes/domains are added, never removed, on deploy. |
| Delete a Worker | ✅ | ✅ | `cfctl workers delete` | |
| Dispatch namespaces (WfP) | ✅ | ✅ | `cfctl dispatch-namespace` | |
| Previews | ✅ | ✅ | `cfctl preview` | ES modules only. |
| `dev` (local server) | ✅ | n/a | `npx wrangler dev` | Local workerd/Miniflare runtime. |
| `types` | ✅ | n/a | `npx wrangler types` | Derived from local workerd. |
| `init` / `setup` | ✅ | 🟡 | `cfctl init` | Offline Worker templates; no C3 frameworks, no `setup`. |
| Workers KV | ✅ | ✅ | `cfctl kv` | |
| R2 | ✅ | ✅ | `cfctl r2` | Plus `ls`, `du`, `usage` with cost, `sync`, temp credentials. |
| D1 | ✅ | 🟡 | `cfctl d1` | Remote only (no `--local`); migrations wrangler-compatible. |
| Queues | ✅ | ✅ | `cfctl queues` | |
| Hyperdrive | ✅ | ✅ | `cfctl hyperdrive` | |
| Vectorize | ✅ | ✅ | `cfctl vectorize` | |
| Secrets Store | ✅ | ✅ | `cfctl secrets-store` | |
| Pipelines / Basin | ✅ | 🟡 | `cfctl basin` | No R2 SQL (`basin sql`), no setup wizard. |
| K2, Artifacts, Agent Memory | ✅ | ✅ | `cfctl k2`, `cfctl artifacts`, `cfctl agent-memory` | |
| Pages | ✅ | 🟡 | `cfctl pages` | Direct Upload deploy; `functions/` isn't bundled, no `pages dev`. |
| Workers AI | ✅ | ✅ | `cfctl ai` | Plus `ai run`, `ai markdown`. |
| AI Search | ✅ | ✅ | `cfctl ai-search` | |
| Workflows | ✅ | ✅ | `cfctl workflows` | |
| Containers | ✅ | 🟡 | `cfctl containers` | `build` wraps docker; no `containers ssh`. |
| Browser Run | ✅ | ✅ | `cfctl browser` | |
| Flagship | ✅ | 🟡 | `cfctl flagship` | Rules as JSON; no rule DSL. |
| Email Routing / Sending | ✅ | ✅ | `cfctl email` | |
| Turnstile | ✅ | ✅ | `cfctl turnstile` | |
| Tunnels | ✅ | ✅ | `cfctl tunnel` | |
| Workers VPC | ✅ | ✅ | `cfctl vpc` | |
| mTLS certificates | ✅ | ✅ | `cfctl cert`, `cfctl mtls-certificate` | |
| Login / whoami | ✅ | 🟡 | `cfctl auth login`, `cfctl whoami` | API token (user or `cfat_` account token), not OAuth; no named profiles. |
| Docs, completion | ✅ | ✅ | `cfctl docs`, `cfctl completion` | |

### Beyond wrangler

| Area | wrangler | cfctl | cfctl command |
|---|:---:|:---:|---|
| Registrar domains (list, expiry, auto-renew) | ❌ | ✅ | `cfctl domains` |
| DNS records | ❌ | ✅ | `cfctl records` |
| DNSSEC, DNS settings, zone file import/export | ❌ | ✅ | `cfctl dns`, `cfctl zones file` |
| Zones: create, delete, pause, settings | ❌ | ✅ | `cfctl zones` |
| SSL/TLS: mode, certificate packs, origin CA | ❌ | ✅ | `cfctl ssl` |
| Cache purge and settings | ❌ | ✅ | `cfctl cache` |
| Rulesets, redirects, transforms, WAF | ❌ | ✅ | `cfctl rulesets`, `cfctl redirects`, `cfctl transform`, `cfctl waf` |
| Firewall access rules, lists, page rules | ❌ | ✅ | `cfctl firewall`, `cfctl lists`, `cfctl page-rules` |
| Load balancing, health checks, waiting rooms, Spectrum | ❌ | ✅ | `cfctl lb`, `cfctl healthchecks`, `cfctl waiting-rooms`, `cfctl spectrum` |
| Analytics presets (zone, Workers, R2, firewall) | ❌ | ✅ | `cfctl analytics` |
| Accounts, members, roles, API tokens, audit logs | ❌ | ✅ | `cfctl accounts`, `cfctl members`, `cfctl roles`, `cfctl tokens`, `cfctl audit-logs` |
| Billing, Logpush, notifications, Access, user | ❌ | ✅ | `cfctl billing`, `cfctl logpush`, `cfctl notifications`, `cfctl access`, `cfctl user` |
| **Every API operation (3,645)** | ❌ | ✅ | `cfctl api <tag> <op>` |
| Raw JSON requests to any path | ❌ | ✅ | `cfctl api request` |
| GraphQL Analytics | ❌ | ✅ | `cfctl graphql` |
| Read-only guard for every command | ❌ | ✅ | `--read-only` / `CFCTL_READONLY=1` |

## Per-command summary

Every wrangler command (as of the wrangler version the maps were made from)
is listed in [`docs/wrangler-map/`](wrangler-map/), one TSV per area, with the
cfctl equivalent, a status, and notes. Counting every command and subcommand:

<!-- BEGIN GENERATED SUMMARY (scripts/gen-wrangler-doc.py) -->
| Area | wrangler commands | ✅ full | 🟡 partial | ➖ n/a |
|---|---:|---:|---:|---:|
| Workers | 39 | 34 | 5 | 0 |
| Storage | 145 | 135 | 7 | 3 |
| Platform | 177 | 152 | 12 | 13 |
| **Total** | **361** | **321** | **24** | **16** |
<!-- END GENERATED SUMMARY -->

**361 wrangler commands → 321 full, 24 partial, 16 not applicable.** None is
missing: every command is either covered or deliberately left to wrangler
because it isn't an API call (local runtime, bundling, type generation,
interactive wizards).

## When to still use wrangler

Install wrangler alongside cfctl (`npx wrangler ...` needs no global install)
for the parts that run on your machine rather than against the API:

| Need | Use | Why cfctl doesn't |
|---|---|---|
| Local dev server for a Worker | `npx wrangler dev` | Runs workerd/Miniflare locally; `cfctl dev` is a stub that says so. |
| Local Pages dev server | `npx wrangler pages dev` | Same local runtime. |
| `worker-configuration.d.ts` | `npx wrangler types` | Derived from the local workerd version and your config. |
| Bundling TypeScript / npm imports | `npx wrangler deploy` (or `cfctl deploy --bundle` with esbuild on `PATH`) | cfctl has no built-in bundler; it uploads pre-built or unbundled ES modules. |
| Pages Functions (`functions/` directory) | `npx wrangler pages functions build`, then `cfctl pages deploy` with the built `_worker.js` | Bundling only. |
| Framework projects (Next.js, Astro, ...) | `npm create cloudflare@latest`, `npx wrangler setup` | cfctl's `init` has plain Worker templates only. |
| D1 against a local database (`--local`) | `npx wrangler d1 execute --local` | cfctl's D1 commands are remote only. |
| R2 SQL (`basin sql`) | `npx wrangler basin sql query` | Separate query endpoint, not the REST API. |
| Interactive Pipelines setup wizard | `npx wrangler basin pipelines setup` | Compose `cfctl basin pipelines streams create`, `cfctl basin pipelines sinks create`, `cfctl basin pipelines create` instead. |
| `containers ssh` | `npx wrangler containers ssh` | Interactive SSH over WebSocket. |
| OAuth login, named auth profiles | `npx wrangler login` | cfctl uses one API token; switch with `CFCTL_TOKEN` or `CFCTL_CONFIG_DIR`. |
| Flagship rule DSL (`serve=...; when=...`) | `npx wrangler flagship ...` | cfctl takes rules as JSON (`--rule-json`). |
| Removing routes/custom domains on deploy, resource auto-provisioning | `npx wrangler deploy` | `cfctl deploy` adds and re-points, never removes; it doesn't create KV/D1/R2 for you. |

## Migrating from wrangler

cfctl reads the same `wrangler.toml` / `wrangler.json` / `wrangler.jsonc`
(including `[env.X]`), so most commands are a straight swap. Log in once with
`cfctl auth login` (account ID, then an API token).

| wrangler | cfctl |
|---|---|
| `wrangler login` | `cfctl auth login` |
| `wrangler whoami` | `cfctl whoami` |
| `wrangler deploy` | `cfctl deploy` (pre-built script, or `--bundle`) |
| `wrangler deploy --dry-run` | `cfctl deploy --dry-run` |
| `wrangler versions upload` | `cfctl versions upload` |
| `wrangler versions deploy <id>@10 <id2>` | `cfctl versions deploy <id>@10 <id2>` |
| `wrangler deployments list` | `cfctl deployments list` |
| `wrangler rollback` | `cfctl rollback` |
| `wrangler tail my-worker --status error` | `cfctl tail my-worker --status error` |
| `wrangler secret put API_KEY` | `cfctl secret put API_KEY` (prompt or stdin) |
| `wrangler secret bulk .env` | `cfctl secret bulk .env` |
| `wrangler kv namespace create CACHE` | `cfctl kv namespace create CACHE` |
| `wrangler kv key put --namespace-id <id> k v` | `cfctl kv key put <namespace> k v` (name or ID) |
| `wrangler kv key get --namespace-id <id> k` | `cfctl kv key get <namespace> k` |
| `wrangler r2 bucket create media` | `cfctl r2 buckets create media` |
| `wrangler r2 object put media/a.png --file a.png` | `cfctl r2 put media/a.png --file a.png` |
| `wrangler r2 object get media/a.png --file a.png` | `cfctl r2 get media/a.png --file a.png` |
| `wrangler d1 execute db --remote --command "SELECT 1"` | `cfctl d1 execute db --command "SELECT 1"` |
| `wrangler d1 migrations apply db --remote` | `cfctl d1 migrations apply db` |
| `wrangler d1 export db --remote --output db.sql` | `cfctl d1 export db --output db.sql` |
| `wrangler queues create jobs` | `cfctl queues create jobs` |
| `wrangler pages deploy ./dist --project-name site` | `cfctl pages deploy ./dist --project-name site` |
| `wrangler ai models` | `cfctl ai models` |
| `wrangler workflows trigger my-wf '{"x":1}'` | `cfctl workflows trigger my-wf '{"x":1}'` |

Things wrangler can't do that you get for free:

```bash
cfctl domains list                                  # Registrar: expiry, auto-renew
cfctl records create example.com --type A --name www --content 192.0.2.1 --proxied
cfctl cache purge example.com --everything
cfctl api search "page shield"                      # find any API operation
cfctl api request GET /accounts/{account_id}/workers/scripts --raw
CFCTL_READONLY=1 cfctl r2 usage                     # guaranteed read-only
```

## Full per-command map

Generated from the TSVs in [`docs/wrangler-map/`](wrangler-map/), which are
the source of truth; edit those and run `make wrangler-doc` to regenerate this
section and the summary above.

<!-- BEGIN GENERATED TABLES (scripts/gen-wrangler-doc.py) -->
### Workers (39 commands: 34 full, 5 partial, 0 n/a)

Source: [`docs/wrangler-map/workers.tsv`](wrangler-map/workers.tsv)

| wrangler | cfctl | status | notes |
|---|---|---|---|
| `wrangler delete` | `cfctl workers delete [name]` | ✅ full | Confirms (or --yes); --force deletes even if other Workers reference it; --dry-run. |
| `wrangler deploy` | `cfctl deploy [script] (= cfctl workers deploy)` | 🟡 partial | Uploads a pre-built ES module or service-worker script with bindings from wrangler.toml/json/jsonc ([env.X] supported) or flags, static assets (upload-session API), DO migrations, then applies crons/routes/custom domains/workers.dev. Unbundled multi-file Workers: relative imports are followed. Bundling only via --bundle with esbuild on PATH (no built-in bundler, no TypeScript without esbuild). Routes/domains are added, never removed. No resource auto-provisioning, containers, or Python package vendoring. |
| `wrangler deployments` | `cfctl deployments (= cfctl workers deployments)` | ✅ full |  |
| `wrangler deployments list` | `cfctl deployments list [name]` | ✅ full | Shows every deployment the API returns, newest first, with version splits. |
| `wrangler deployments status` | `cfctl deployments status [name]` | ✅ full |  |
| `wrangler dispatch-namespace` | `cfctl dispatch-namespace (= cfctl workers dispatch-namespace)` | ✅ full | Needs the Workers for Platforms add-on; also 'scripts <ns>' to list a namespace's scripts. |
| `wrangler dispatch-namespace list` | `cfctl dispatch-namespace list` | ✅ full |  |
| `wrangler dispatch-namespace get` | `cfctl dispatch-namespace get <name>` | ✅ full |  |
| `wrangler dispatch-namespace create` | `cfctl dispatch-namespace create <name>` | ✅ full |  |
| `wrangler dispatch-namespace delete` | `cfctl dispatch-namespace delete <name>` | ✅ full | Confirms (or --yes). |
| `wrangler dispatch-namespace rename` | `cfctl dispatch-namespace rename <old> <new>` | ✅ full |  |
| `wrangler preview` | `cfctl workers preview deploy [script] --preview <name>` | 🟡 partial | Creates the Preview if missing, then a preview deployment (JSON modules API). ES modules only; preview name defaults to the git branch. Also preview list\|get\|deployments. |
| `wrangler preview delete` | `cfctl workers preview delete --preview <name>` | ✅ full | Confirms (or --yes). |
| `wrangler preview secret` | `cfctl workers preview secret` | ✅ full |  |
| `wrangler preview secret put` | `cfctl workers preview secret put <KEY> --preview <name>` | ✅ full | Value from hidden prompt or stdin; patches the latest preview deployment's env. |
| `wrangler preview secret delete` | `cfctl workers preview secret delete <KEY> --preview <name>` | ✅ full | Confirms (or --yes). |
| `wrangler preview secret list` | `cfctl workers preview secret list --preview <name>` | ✅ full | Names only. |
| `wrangler preview secret bulk` | `cfctl workers preview secret bulk [file|-] --preview <name>` | ✅ full | JSON object or .env; null deletes. |
| `wrangler preview base-config` | `cfctl workers preview base-config` | ✅ full | Secrets only (the base config's other fields: cfctl api workers edit-worker). |
| `wrangler preview base-config secret` | `cfctl workers preview base-config secret put|delete|list|bulk` | ✅ full | Patches previews_base_config.env on the Worker. |
| `wrangler rollback` | `cfctl rollback [version-id] (= cfctl workers rollback)` | ✅ full | Defaults to the previous deployment's versions; confirms (or --yes); sends force=true like wrangler. |
| `wrangler secret` | `cfctl secret (= cfctl workers secret)` | ✅ full |  |
| `wrangler secret put` | `cfctl secret put <KEY> --name <worker>` | ✅ full | Hidden prompt on a TTY, else stdin; value never printed. |
| `wrangler secret delete` | `cfctl secret delete <KEY> --name <worker>` | ✅ full | Confirms (or --yes). |
| `wrangler secret list` | `cfctl secret list --name <worker>` | ✅ full | Names and types only. |
| `wrangler secret bulk` | `cfctl secret bulk [file|-] --name <worker>` | ✅ full | JSON object or .env; null deletes (confirms); max 100; one version via secrets-bulk. |
| `wrangler tail` | `cfctl tail [name] (= cfctl workers tail)` | 🟡 partial | --format pretty\|json, --status, --method, --header, --ip, --search, --sampling-rate; Ctrl-C deletes the tail. No '--ip self', no auto-reconnect when the tail expires, no --version-id. |
| `wrangler triggers` | `cfctl triggers (= cfctl workers triggers)` | ✅ full | Adds 'triggers list' (crons, routes on every zone, custom domains, workers.dev). |
| `wrangler triggers deploy` | `cfctl triggers deploy [name]` | 🟡 partial | Applies crons (replaced), routes and custom domains (added/re-pointed, never removed), workers.dev from config or flags. |
| `wrangler versions` | `cfctl versions (= cfctl workers versions)` | ✅ full |  |
| `wrangler versions view` | `cfctl versions view <version-id|prefix|latest> --name <worker>` | ✅ full |  |
| `wrangler versions list` | `cfctl versions list [name]` | ✅ full | --limit (default 10, 0 = all), paginated. |
| `wrangler versions upload` | `cfctl versions upload [script]` | 🟡 partial | Same inputs and caveats as deploy (bundling only via --bundle + esbuild); --message, --tag, --preview-alias; never touches triggers. |
| `wrangler versions deploy` | `cfctl versions deploy <version[@pct]>... --name <worker>` | ✅ full | Non-interactive: 1 or 2 versions, prefixes or 'latest', remainder split automatically; --message, --force. |
| `wrangler versions secret` | `cfctl versions secret` | ✅ full | Creates a new undeployed version via PATCH /workers/workers/{id}/versions/latest, inheriting every other binding. |
| `wrangler versions secret put` | `cfctl versions secret put <KEY> --name <worker>` | ✅ full |  |
| `wrangler versions secret bulk` | `cfctl versions secret bulk [file|-] --name <worker>` | ✅ full | JSON object or .env; null removes (confirms). |
| `wrangler versions secret delete` | `cfctl versions secret delete <KEY> --name <worker>` | ✅ full | Confirms (or --yes). |
| `wrangler versions secret list` | `cfctl versions secret list --name <worker>` | ✅ full | Secrets in the latest version. |

### Storage (145 commands: 135 full, 7 partial, 3 n/a)

Source: [`docs/wrangler-map/storage.tsv`](wrangler-map/storage.tsv)

| wrangler | cfctl | status | notes |
|---|---|---|---|
| `wrangler kv` | `cfctl kv` | ✅ full | group: namespace, key, bulk |
| `wrangler kv namespace` | `cfctl kv namespace` | ✅ full | also `get`; namespace by title or ID |
| `wrangler kv namespace create` | `cfctl kv namespace create` | ✅ full | --jurisdiction; prints binding snippet |
| `wrangler kv namespace list` | `cfctl kv namespace list` | ✅ full |  |
| `wrangler kv namespace delete` | `cfctl kv namespace delete` | ✅ full | confirms (--yes) |
| `wrangler kv namespace rename` | `cfctl kv namespace rename` | ✅ full |  |
| `wrangler kv key` | `cfctl kv key` | ✅ full |  |
| `wrangler kv key put` | `cfctl kv key put` | ✅ full | value arg, - (stdin), or --path; --ttl, --expiration, --metadata (multipart) |
| `wrangler kv key list` | `cfctl kv key list` | ✅ full | follows the cursor; --prefix, --limit |
| `wrangler kv key get` | `cfctl kv key get` | ✅ full | raw value to stdout or -o file; --metadata |
| `wrangler kv key delete` | `cfctl kv key delete` | ✅ full | confirms (--yes) |
| `wrangler kv bulk` | `cfctl kv bulk` | ✅ full |  |
| `wrangler kv bulk get` | `cfctl kv bulk get` | ✅ full | JSON file or keys as args; batches of 100; --metadata, --type json |
| `wrangler kv bulk put` | `cfctl kv bulk put` | ✅ full | batches of 10,000 |
| `wrangler kv bulk delete` | `cfctl kv bulk delete` | ✅ full | batches of 10,000; confirms; reports unsuccessful keys |
| `wrangler r2` | `cfctl r2` | ✅ full | plus ls/stat/du/usage/sync/temp-credentials beyond wrangler |
| `wrangler r2 object` | `cfctl r2 object (also r2 get/put/rm/stat)` | ✅ full | REST API; S3 multipart above --multipart-threshold |
| `wrangler r2 object get` | `cfctl r2 get / r2 object get` | ✅ full | streams to stdout or --file |
| `wrangler r2 object put` | `cfctl r2 put / r2 object put` | ✅ full | content type detection; stdin; >100 MB or extra metadata → S3 multipart with temp credentials |
| `wrangler r2 object delete` | `cfctl r2 rm / r2 object delete` | ✅ full | multiple keys; -r prefix with confirmation; --dry-run |
| `wrangler r2 bucket` | `cfctl r2 buckets (alias bucket)` | ✅ full |  |
| `wrangler r2 bucket create` | `cfctl r2 buckets create` | ✅ full | --location, --storage-class, --jurisdiction |
| `wrangler r2 bucket update` | `cfctl r2 buckets update` | ✅ full | --storage-class (default storage class) |
| `wrangler r2 bucket list` | `cfctl r2 buckets list` | ✅ full |  |
| `wrangler r2 bucket info` | `cfctl r2 buckets get (alias info)` | ✅ full | object count and size from GraphQL |
| `wrangler r2 bucket delete` | `cfctl r2 buckets delete` | ✅ full | --force empties the bucket first (confirms) |
| `wrangler r2 bucket sippy` | `cfctl r2 buckets sippy get|enable|disable` | ✅ full | aws, gcs (key file), s3, azure providers |
| `wrangler r2 bucket notification` | `cfctl r2 buckets notification list|create|delete` | ✅ full | queue by name or ID; object-create/object-delete event types |
| `wrangler r2 bucket domain` | `cfctl r2 buckets domain list|get|add|update|remove` | ✅ full | zone found from the domain |
| `wrangler r2 bucket dev-url` | `cfctl r2 buckets dev-url get|enable|disable` | ✅ full |  |
| `wrangler r2 bucket local-uploads` | `cfctl r2 buckets local-uploads get|enable|disable` | ✅ full |  |
| `wrangler r2 bucket lifecycle` | `cfctl r2 buckets lifecycle list|add|remove|set` | ✅ full |  |
| `wrangler r2 bucket cors` | `cfctl r2 buckets cors list|set|delete` | ✅ full |  |
| `wrangler r2 bucket lock` | `cfctl r2 buckets lock list|add|remove|set` | ✅ full |  |
| `wrangler d1` | `cfctl d1` | ✅ full | remote only (no --local; use wrangler/Miniflare for local) |
| `wrangler d1 create` | `cfctl d1 create` | ✅ full | --location, --jurisdiction, --read-replication; prints binding snippet |
| `wrangler d1 info` | `cfctl d1 info` | ✅ full | includes 24h query/row counts from GraphQL |
| `wrangler d1 list` | `cfctl d1 list` | ✅ full |  |
| `wrangler d1 delete` | `cfctl d1 delete` | ✅ full | confirms (--yes) |
| `wrangler d1 execute` | `cfctl d1 execute` | 🟡 partial | remote only; --command/--file/--param/--json; files over 5 MB use the import API; no --local |
| `wrangler d1 export` | `cfctl d1 export` | 🟡 partial | remote only; --no-data, --no-schema, --table |
| `wrangler d1 time-travel` | `cfctl d1 time-travel` | ✅ full |  |
| `wrangler d1 time-travel info` | `cfctl d1 time-travel info` | ✅ full |  |
| `wrangler d1 time-travel restore` | `cfctl d1 time-travel restore` | ✅ full | prints the undo bookmark |
| `wrangler d1 migrations` | `cfctl d1 migrations` | 🟡 partial | wrangler-compatible files and d1_migrations table; reads --migrations-dir, not wrangler.toml |
| `wrangler d1 migrations create` | `cfctl d1 migrations create` | ✅ full |  |
| `wrangler d1 migrations list` | `cfctl d1 migrations list` | 🟡 partial | remote only |
| `wrangler d1 migrations apply` | `cfctl d1 migrations apply` | 🟡 partial | remote only; prints a Time Travel bookmark first |
| `wrangler d1 insights` | `cfctl d1 insights` | ✅ full | GraphQL d1QueriesAdaptiveGroups; --since, --sort-by, --limit |
| `wrangler agent-memory` | `cfctl agent-memory` | ✅ full | group; also profile, memories (list/get/delete/remember/recall/ingest), session delete |
| `wrangler agent-memory namespace` | `cfctl agent-memory namespace` | ✅ full |  |
| `wrangler agent-memory namespace create` | `cfctl agent-memory namespace create` | ✅ full |  |
| `wrangler agent-memory namespace list` | `cfctl agent-memory namespace list` | ✅ full | private beta: this account gets HTTP 401 (code 10018) |
| `wrangler agent-memory namespace get` | `cfctl agent-memory namespace get` | ✅ full |  |
| `wrangler agent-memory namespace delete` | `cfctl agent-memory namespace delete` | ✅ full | confirms; --yes |
| `wrangler queues` | `cfctl queues` | ✅ full | also send, send-batch, pull, ack, peek, metrics, purge status (beyond wrangler) |
| `wrangler queues list` | `cfctl queues list` | ✅ full |  |
| `wrangler queues create` | `cfctl queues create` | ✅ full | --delivery-delay, --message-retention-period, --jurisdiction, --data |
| `wrangler queues update` | `cfctl queues update` | ✅ full | PATCH; --name renames |
| `wrangler queues delete` | `cfctl queues delete` | ✅ full | confirms; --yes |
| `wrangler queues info` | `cfctl queues get (alias info)` | ✅ full |  |
| `wrangler queues consumer` | `cfctl queues consumer` | ✅ full | also consumer get, consumer update |
| `wrangler queues consumer add` | `cfctl queues consumer add` | ✅ full | worker consumer; --batch-timeout seconds → max_wait_time_ms |
| `wrangler queues consumer remove` | `cfctl queues consumer remove` | ✅ full | by script name or consumer ID |
| `wrangler queues consumer list` | `cfctl queues consumer list` | ✅ full |  |
| `wrangler queues consumer http` | `cfctl queues consumer http add/remove` | ✅ full | http_pull consumers |
| `wrangler queues consumer worker` | `cfctl queues consumer worker add/remove` | ✅ full | same as consumer add/remove |
| `wrangler queues pause-delivery` | `cfctl queues pause-delivery` | ✅ full | PATCH settings.delivery_paused (other settings kept) |
| `wrangler queues resume-delivery` | `cfctl queues resume-delivery` | ✅ full |  |
| `wrangler queues purge` | `cfctl queues purge` | ✅ full | confirms; 'queues purge status' shows progress |
| `wrangler queues subscription` | `cfctl queues subscription` | ✅ full |  |
| `wrangler queues subscription create` | `cfctl queues subscription create` | ✅ full | --source/--events/--name + source-specific flags |
| `wrangler queues subscription list` | `cfctl queues subscription list [queue]` | ✅ full | optional queue filter |
| `wrangler queues subscription get` | `cfctl queues subscription get <id>` | ✅ full | takes the subscription ID (wrangler: <queue> --id) |
| `wrangler queues subscription delete` | `cfctl queues subscription delete <id>` | ✅ full | confirms |
| `wrangler queues subscription update` | `cfctl queues subscription update <id>` | ✅ full |  |
| `wrangler artifacts` | `cfctl artifacts` | ✅ full |  |
| `wrangler artifacts namespaces` | `cfctl artifacts namespaces` | ✅ full | also create, delete (API-backed, beyond wrangler) |
| `wrangler artifacts namespaces list` | `cfctl artifacts namespaces list` | ✅ full | private beta: this account gets HTTP 403 (code 10004) |
| `wrangler artifacts namespaces get` | `cfctl artifacts namespaces get` | ✅ full |  |
| `wrangler artifacts repos` | `cfctl artifacts repos` | ✅ full | also fork, import, log, file, blob/commit/tree, tokens, revoke-token |
| `wrangler artifacts repos create` | `cfctl artifacts repos create` | ✅ full | --read-only-repo (not --read-only, which is the global guard) |
| `wrangler artifacts repos list` | `cfctl artifacts repos list` | ✅ full |  |
| `wrangler artifacts repos get` | `cfctl artifacts repos get` | ✅ full |  |
| `wrangler artifacts repos delete` | `cfctl artifacts repos delete` | ✅ full | confirms |
| `wrangler artifacts repos issue-token` | `cfctl artifacts repos issue-token` | ✅ full | prints the repo token once |
| `wrangler basin` | `cfctl basin` | 🟡 partial | basin sql is not REST-backed |
| `wrangler basin sql` | — | ➖ n/a | R2 SQL runs on a separate query endpoint, not the Cloudflare REST API; use wrangler |
| `wrangler basin sql query` | — | ➖ n/a | see basin sql |
| `wrangler basin catalog` | `cfctl basin catalog` | ✅ full | newCatalogCmd also backs 'cfctl r2 bucket catalog' (r2-catalog) |
| `wrangler basin catalog enable` | `cfctl basin catalog enable` | ✅ full |  |
| `wrangler basin catalog disable` | `cfctl basin catalog disable` | ✅ full | confirms |
| `wrangler basin catalog get` | `cfctl basin catalog get` | ✅ full | also credential set/status, maintenance get/set, namespaces, tables, table |
| `wrangler basin catalog compaction` | `cfctl basin catalog compaction enable/disable` | ✅ full | --target-size |
| `wrangler basin catalog snapshot-expiration` | `cfctl basin catalog snapshot-expiration enable/disable` | ✅ full | --max-age, --min-snapshots |
| `wrangler basin pipelines` | `cfctl basin pipelines` | ✅ full | v1 API; also validate-sql |
| `wrangler basin pipelines setup` | — | ➖ n/a | interactive wizard; compose 'streams create', 'sinks create', 'pipelines create' |
| `wrangler basin pipelines create` | `cfctl basin pipelines create` | ✅ full | --sql / --sql-file |
| `wrangler basin pipelines list` | `cfctl basin pipelines list` | ✅ full | Workers Paid: this account gets HTTP 403 (code 1021) |
| `wrangler basin pipelines get` | `cfctl basin pipelines get` | ✅ full |  |
| `wrangler basin pipelines update` | `cfctl basin pipelines update --legacy` | 🟡 partial | legacy pipelines only (PUT --data); v1 pipelines have no update endpoint |
| `wrangler basin pipelines delete` | `cfctl basin pipelines delete` | ✅ full | confirms |
| `wrangler basin pipelines streams` | `cfctl basin pipelines streams list/get/create/update/delete` | ✅ full |  |
| `wrangler basin pipelines sinks` | `cfctl basin pipelines sinks list/get/create/delete` | ✅ full | r2 and r2_data_catalog flags; --data for anything else |
| `wrangler hyperdrive` | `cfctl hyperdrive` | ✅ full | also restart |
| `wrangler hyperdrive create` | `cfctl hyperdrive create` | ✅ full | --connection-string or --origin-*; Access (tunnel) and VPC origins; caching, mTLS |
| `wrangler hyperdrive delete` | `cfctl hyperdrive delete` | ✅ full | confirms |
| `wrangler hyperdrive get` | `cfctl hyperdrive get` | ✅ full |  |
| `wrangler hyperdrive list` | `cfctl hyperdrive list` | ✅ full |  |
| `wrangler hyperdrive planetscale` | `cfctl hyperdrive planetscale` | ✅ full |  |
| `wrangler hyperdrive planetscale signature` | `cfctl hyperdrive planetscale signature` | ✅ full |  |
| `wrangler hyperdrive update` | `cfctl hyperdrive update` | ✅ full | PATCH |
| `wrangler k2` | `cfctl k2` | ✅ full |  |
| `wrangler k2 streams` | `cfctl k2 streams` | ✅ full | also update, subscriptions |
| `wrangler k2 streams create` | `cfctl k2 streams create` | ✅ full |  |
| `wrangler k2 streams get` | `cfctl k2 streams get` | ✅ full |  |
| `wrangler k2 streams list` | `cfctl k2 streams list` | ✅ full | Workers Paid: this account gets HTTP 403 (code 1021) |
| `wrangler k2 streams delete` | `cfctl k2 streams delete` | ✅ full | confirms |
| `wrangler secrets-store` | `cfctl secrets-store` | ✅ full | also quota |
| `wrangler secrets-store store` | `cfctl secrets-store store` | ✅ full | also get |
| `wrangler secrets-store store create` | `cfctl secrets-store store create` | ✅ full |  |
| `wrangler secrets-store store delete` | `cfctl secrets-store store delete` | ✅ full | confirms; --force cascades |
| `wrangler secrets-store store list` | `cfctl secrets-store store list` | ✅ full |  |
| `wrangler secrets-store secret` | `cfctl secrets-store secret` | ✅ full | values are never printed |
| `wrangler secrets-store secret create` | `cfctl secrets-store secret create` | ✅ full | value from no-echo prompt, stdin, --value-file, or --value |
| `wrangler secrets-store secret list` | `cfctl secrets-store secret list` | ✅ full |  |
| `wrangler secrets-store secret get` | `cfctl secrets-store secret get` | ✅ full |  |
| `wrangler secrets-store secret update` | `cfctl secrets-store secret update` | ✅ full |  |
| `wrangler secrets-store secret delete` | `cfctl secrets-store secret delete` | ✅ full | confirms |
| `wrangler secrets-store secret duplicate` | `cfctl secrets-store secret duplicate` | ✅ full | keeps the original's scopes unless --scopes |
| `wrangler vectorize` | `cfctl vectorize` | ✅ full | v2 API |
| `wrangler vectorize create` | `cfctl vectorize create` | ✅ full | --dimensions/--metric or --preset |
| `wrangler vectorize delete` | `cfctl vectorize delete` | ✅ full | confirms |
| `wrangler vectorize get` | `cfctl vectorize get` | ✅ full |  |
| `wrangler vectorize list` | `cfctl vectorize list` | ✅ full |  |
| `wrangler vectorize list-vectors` | `cfctl vectorize list-vectors` | ✅ full | --all follows the cursor |
| `wrangler vectorize query` | `cfctl vectorize query` | ✅ full | --vector / --vector-file, --filter |
| `wrangler vectorize insert` | `cfctl vectorize insert` | ✅ full | NDJSON, batches of 5000 lines |
| `wrangler vectorize upsert` | `cfctl vectorize upsert` | ✅ full |  |
| `wrangler vectorize get-vectors` | `cfctl vectorize get-vectors` | ✅ full |  |
| `wrangler vectorize delete-vectors` | `cfctl vectorize delete-vectors` | ✅ full | confirms |
| `wrangler vectorize info` | `cfctl vectorize info` | ✅ full |  |
| `wrangler vectorize create-metadata-index` | `cfctl vectorize create-metadata-index` | ✅ full | also 'vectorize metadata-index create' |
| `wrangler vectorize list-metadata-index` | `cfctl vectorize list-metadata-index` | ✅ full |  |
| `wrangler vectorize delete-metadata-index` | `cfctl vectorize delete-metadata-index` | ✅ full | confirms |

### Platform (177 commands: 152 full, 12 partial, 13 n/a)

Source: [`docs/wrangler-map/platform.tsv`](wrangler-map/platform.tsv)

| wrangler | cfctl | status | notes |
|---|---|---|---|
| `wrangler ai` | `cfctl ai` | ✅ full | + ai run, ai markdown, ai tasks, ai authors |
| `wrangler ai finetune` | `cfctl ai finetune` | ✅ full | + public, delete |
| `wrangler ai finetune create` | `cfctl ai finetune create` | ✅ full | uploads adapter_config.json + adapter_model.safetensors |
| `wrangler ai finetune list` | `cfctl ai finetune list` | ✅ full |  |
| `wrangler ai models` | `cfctl ai models` | ✅ full | + search, get |
| `wrangler ai models list` | `cfctl ai models list` | ✅ full | --task/--author/--search |
| `wrangler ai models schema` | `cfctl ai models schema` | ✅ full |  |
| `wrangler ai-search` | `cfctl ai-search` | ✅ full | + items, tokens |
| `wrangler ai-search create` | `cfctl ai-search create` | ✅ full |  |
| `wrangler ai-search delete` | `cfctl ai-search delete` | ✅ full |  |
| `wrangler ai-search get` | `cfctl ai-search get` | ✅ full |  |
| `wrangler ai-search jobs` | `cfctl ai-search jobs` | ✅ full |  |
| `wrangler ai-search jobs cancel` | `cfctl ai-search jobs cancel` | ✅ full |  |
| `wrangler ai-search jobs create` | `cfctl ai-search jobs create` | ✅ full |  |
| `wrangler ai-search jobs get` | `cfctl ai-search jobs get` | ✅ full |  |
| `wrangler ai-search jobs list` | `cfctl ai-search jobs list` | ✅ full |  |
| `wrangler ai-search jobs logs` | `cfctl ai-search jobs logs` | ✅ full |  |
| `wrangler ai-search list` | `cfctl ai-search list` | ✅ full |  |
| `wrangler ai-search namespace` | `cfctl ai-search namespace` | ✅ full |  |
| `wrangler ai-search namespace create` | `cfctl ai-search namespace create` | ✅ full |  |
| `wrangler ai-search namespace delete` | `cfctl ai-search namespace delete` | ✅ full |  |
| `wrangler ai-search namespace get` | `cfctl ai-search namespace get` | ✅ full |  |
| `wrangler ai-search namespace list` | `cfctl ai-search namespace list` | ✅ full |  |
| `wrangler ai-search namespace update` | `cfctl ai-search namespace update` | ✅ full |  |
| `wrangler ai-search search` | `cfctl ai-search search` | ✅ full |  |
| `wrangler ai-search stats` | `cfctl ai-search stats` | ✅ full |  |
| `wrangler ai-search update` | `cfctl ai-search update` | ✅ full |  |
| `wrangler auth` | `cfctl auth` | 🟡 partial | token-based: login/logout/status/setup; no OAuth or named profiles |
| `wrangler auth activate` | — | ➖ n/a | per-directory profiles; use CFCTL_CONFIG_DIR or CFCTL_TOKEN per project |
| `wrangler auth create` | — | ➖ n/a | named OAuth profiles; cfctl uses one API token (CFCTL_TOKEN / CFCTL_CONFIG_DIR to switch) |
| `wrangler auth deactivate` | — | ➖ n/a | see auth activate |
| `wrangler auth delete` | — | ➖ n/a | see auth create |
| `wrangler auth keyring` | — | ➖ n/a | cfctl stores an API token in ~/.config/cfctl/token (0600), not an OAuth keychain entry |
| `wrangler auth list` | — | ➖ n/a | see auth create |
| `wrangler auth token` | `cfctl auth status` | 🟡 partial | cfctl never prints the stored token; status shows its source and type |
| `wrangler browser` | `cfctl browser` | ✅ full | + get, screenshot, pdf, markdown, content, links |
| `wrangler browser close` | `cfctl browser close` | ✅ full |  |
| `wrangler browser create` | `cfctl browser create` | ✅ full | prints the DevTools URL instead of opening a browser |
| `wrangler browser list` | `cfctl browser list` | ✅ full |  |
| `wrangler browser view` | `cfctl browser view` | ✅ full | prints the DevTools URL; --target |
| `wrangler cert` | `cfctl cert` | ✅ full | + get, associations |
| `wrangler cert delete` | `cfctl cert delete` | ✅ full |  |
| `wrangler cert list` | `cfctl cert list` | ✅ full |  |
| `wrangler cert upload` | `cfctl cert upload` | ✅ full |  |
| `wrangler cert upload certificate-authority` | `cfctl cert upload certificate-authority` | ✅ full |  |
| `wrangler cert upload mtls-certificate` | `cfctl cert upload mtls-certificate` | ✅ full |  |
| `wrangler complete` | `cfctl completion` | ✅ full | bash, zsh, fish, powershell |
| `wrangler containers` | `cfctl containers` | ✅ full | + versions |
| `wrangler containers build` | `cfctl containers build` | 🟡 partial | wraps docker build --platform linux/amd64 (+ --push); no wrangler.jsonc container config |
| `wrangler containers delete` | `cfctl containers delete` | ✅ full |  |
| `wrangler containers images` | `cfctl containers images` | ✅ full |  |
| `wrangler containers images delete` | `cfctl containers images delete` | ✅ full | manifest delete + layer GC |
| `wrangler containers images list` | `cfctl containers images list` | ✅ full | registry v2 catalog with short-lived pull creds (a POST, so not in --read-only) |
| `wrangler containers info` | `cfctl containers info` | ✅ full | name or ID |
| `wrangler containers instances` | `cfctl containers instances` | ✅ full |  |
| `wrangler containers list` | `cfctl containers list` | ✅ full |  |
| `wrangler containers push` | `cfctl containers push` | ✅ full | docker login via stdin with short-lived API credentials, tag, push |
| `wrangler containers registries` | `cfctl containers registries` | ✅ full |  |
| `wrangler containers registries configure` | `cfctl containers registries configure` | 🟡 partial | auth config via --data; no secrets-store prompting |
| `wrangler containers registries credentials` | `cfctl containers registries credentials` | ✅ full | password redacted unless --reveal |
| `wrangler containers registries delete` | `cfctl containers registries delete` | ✅ full |  |
| `wrangler containers registries list` | `cfctl containers registries list` | ✅ full |  |
| `wrangler containers ssh` | `cfctl containers ssh` | ➖ n/a | interactive SSH over WebSocket; stub points to wrangler |
| `wrangler dev` | `cfctl dev` | ➖ n/a | local workerd/Miniflare runtime; stub points to npx wrangler dev |
| `wrangler docs` | `cfctl docs` | ✅ full | renders the embedded README/docs offline instead of opening a browser; --search finds topics |
| `wrangler email` | `cfctl email` | ✅ full |  |
| `wrangler email routing` | `cfctl email routing` | ✅ full | + catch-all get\|set |
| `wrangler email routing addresses` | `cfctl email routing addresses list|get|create|delete` | ✅ full |  |
| `wrangler email routing disable` | `cfctl email routing disable` | ✅ full | confirms |
| `wrangler email routing dns` | `cfctl email routing dns get|unlock` | ✅ full |  |
| `wrangler email routing enable` | `cfctl email routing enable` | ✅ full |  |
| `wrangler email routing list` | `cfctl email routing list` | ✅ full |  |
| `wrangler email routing rules` | `cfctl email routing rules list|get|create|update|delete` | ✅ full | --to with --forward/--worker/--drop |
| `wrangler email routing settings` | `cfctl email routing settings` | ✅ full |  |
| `wrangler email sending` | `cfctl email sending` | ✅ full |  |
| `wrangler email sending disable` | `cfctl email sending disable` | ✅ full |  |
| `wrangler email sending dns` | `cfctl email sending dns get` | ✅ full |  |
| `wrangler email sending enable` | `cfctl email sending enable` | ✅ full |  |
| `wrangler email sending list` | `cfctl email sending list` | ✅ full |  |
| `wrangler email sending send` | `cfctl email sending send` | ✅ full | attachments, headers, cc/bcc, reply-to |
| `wrangler email sending send-raw` | `cfctl email sending send-raw` | ✅ full |  |
| `wrangler email sending settings` | `cfctl email sending settings` | ✅ full |  |
| `wrangler flagship` | `cfctl flagship` | ✅ full |  |
| `wrangler flagship apps` | `cfctl flagship apps` | ✅ full |  |
| `wrangler flagship apps create` | `cfctl flagship apps create` | ✅ full |  |
| `wrangler flagship apps delete` | `cfctl flagship apps delete` | ✅ full |  |
| `wrangler flagship apps get` | `cfctl flagship apps get` | ✅ full |  |
| `wrangler flagship apps list` | `cfctl flagship apps list` | ✅ full |  |
| `wrangler flagship apps ls` | `cfctl flagship apps ls` | ✅ full | alias of list |
| `wrangler flagship apps rm` | `cfctl flagship apps rm` | ✅ full | alias of delete |
| `wrangler flagship apps update` | `cfctl flagship apps update` | ✅ full |  |
| `wrangler flagship flags` | `cfctl flagship flags` | ✅ full |  |
| `wrangler flagship flags changelog` | `cfctl flagship flags changelog` | ✅ full |  |
| `wrangler flagship flags create` | `cfctl flagship flags create` | 🟡 partial | --variation/--rule-json; no "serve=...; when=..." rule DSL |
| `wrangler flagship flags delete` | `cfctl flagship flags delete` | ✅ full |  |
| `wrangler flagship flags disable` | `cfctl flagship flags disable` | ✅ full |  |
| `wrangler flagship flags enable` | `cfctl flagship flags enable` | ✅ full |  |
| `wrangler flagship flags eval` | `cfctl flagship flags eval` | ✅ full | alias |
| `wrangler flagship flags evaluate` | `cfctl flagship flags evaluate` | ✅ full |  |
| `wrangler flagship flags get` | `cfctl flagship flags get` | ✅ full |  |
| `wrangler flagship flags history` | `cfctl flagship flags history` | ✅ full | alias of changelog |
| `wrangler flagship flags inspect` | `cfctl flagship flags inspect` | ✅ full | alias of get |
| `wrangler flagship flags list` | `cfctl flagship flags list` | ✅ full |  |
| `wrangler flagship flags ls` | `cfctl flagship flags ls` | ✅ full | alias of list |
| `wrangler flagship flags pull` | `cfctl flagship flags pull` | 🟡 partial | writes a portable JSON file; wrangler imports into its local Miniflare flag store |
| `wrangler flagship flags rm` | `cfctl flagship flags rm` | ✅ full | alias of delete |
| `wrangler flagship flags rollout` | `cfctl flagship flags rollout` | ✅ full |  |
| `wrangler flagship flags rules` | `cfctl flagship flags rules` | 🟡 partial | replace rules with --rule-json; no rule DSL or add/remove-by-priority |
| `wrangler flagship flags set` | `cfctl flagship flags set` | ✅ full |  |
| `wrangler flagship flags split` | `cfctl flagship flags split` | ✅ full |  |
| `wrangler flagship flags update` | `cfctl flagship flags update` | 🟡 partial | full flag via --data; use enable/disable/set/rollout/split/rules for edits |
| `wrangler init` | `cfctl init` | 🟡 partial | scaffolds worker/scheduled/assets templates offline; no C3 framework templates or npm install |
| `wrangler login` | `cfctl auth login` | ✅ full | API token (hidden prompt or stdin) instead of OAuth |
| `wrangler logout` | `cfctl auth logout` | ✅ full |  |
| `wrangler mtls-certificate` | `cfctl mtls-certificate` | ✅ full | + get, associations |
| `wrangler mtls-certificate delete` | `cfctl mtls-certificate delete` | ✅ full |  |
| `wrangler mtls-certificate list` | `cfctl mtls-certificate list` | ✅ full |  |
| `wrangler mtls-certificate upload` | `cfctl mtls-certificate upload` | ✅ full |  |
| `wrangler pages` | `cfctl pages` | ✅ full |  |
| `wrangler pages deploy` | `cfctl pages deploy` | 🟡 partial | Direct Upload (hash, check-missing, upload, upsert, deployment) incl. _headers/_redirects/_routes.json and a pre-built _worker.js; functions/ is not bundled |
| `wrangler pages deployment` | `cfctl pages deployment` | ✅ full | + get, retry, rollback, logs |
| `wrangler pages deployment create` | `cfctl pages deploy` | 🟡 partial | same as pages deploy |
| `wrangler pages deployment delete` | `cfctl pages deployment delete` | ✅ full | --force for aliased deployments |
| `wrangler pages deployment list` | `cfctl pages deployment list` | ✅ full | --env production\|preview |
| `wrangler pages deployment tail` | `cfctl pages deployment tail` | ✅ full | WebSocket trace-v1; --status/--method/--header/--ip/--search/--sampling-rate, --format json |
| `wrangler pages dev` | `cfctl pages dev` | ➖ n/a | local runtime; stub points to npx wrangler pages dev |
| `wrangler pages download` | `cfctl pages download` | ✅ full |  |
| `wrangler pages download config` | `cfctl pages download config` | 🟡 partial | writes wrangler.jsonc (vars + common bindings); output dir is a guess |
| `wrangler pages functions` | `cfctl pages functions` | ➖ n/a | bundling only |
| `wrangler pages functions build` | `cfctl pages functions build` | ➖ n/a | esbuild bundling of functions/; deploy a pre-built _worker.js with cfctl pages deploy |
| `wrangler pages project` | `cfctl pages project` | ✅ full | + get, edit, purge-build-cache |
| `wrangler pages project create` | `cfctl pages project create` | ✅ full | Direct Upload projects; Git-connected needs dashboard OAuth (or --data source) |
| `wrangler pages project delete` | `cfctl pages project delete` | ✅ full | confirms; --yes |
| `wrangler pages project list` | `cfctl pages project list` | ✅ full |  |
| `wrangler pages secret` | `cfctl pages secret` | ✅ full |  |
| `wrangler pages secret bulk` | `cfctl pages secret bulk` | ✅ full | JSON object file or stdin |
| `wrangler pages secret delete` | `cfctl pages secret delete` | ✅ full |  |
| `wrangler pages secret list` | `cfctl pages secret list` | ✅ full | names only |
| `wrangler pages secret put` | `cfctl pages secret put` | ✅ full | value from hidden prompt or stdin; --env |
| `wrangler setup` | `cfctl setup` | ➖ n/a | framework autoconfig rewrites local build config; stub points to wrangler setup |
| `wrangler tunnel` | `cfctl tunnel` | ✅ full | + token, config, connections, cleanup, route, vnet |
| `wrangler tunnel create` | `cfctl tunnel create` | ✅ full | remotely managed by default; --local writes the credentials file (0600) |
| `wrangler tunnel delete` | `cfctl tunnel delete` | ✅ full |  |
| `wrangler tunnel info` | `cfctl tunnel info` | ✅ full |  |
| `wrangler tunnel list` | `cfctl tunnel list` | ✅ full |  |
| `wrangler tunnel quick-start` | `cfctl tunnel quick-start` | ✅ full | execs cloudflared tunnel --url |
| `wrangler tunnel run` | `cfctl tunnel run` | ✅ full | execs cloudflared with TUNNEL_TOKEN in the environment |
| `wrangler turnstile` | `cfctl turnstile` | ✅ full | + rotate-secret |
| `wrangler turnstile widget` | `cfctl turnstile widget` | ✅ full |  |
| `wrangler turnstile widget create` | `cfctl turnstile widget create` | ✅ full |  |
| `wrangler turnstile widget delete` | `cfctl turnstile widget delete` | ✅ full |  |
| `wrangler turnstile widget get` | `cfctl turnstile widget get` | ✅ full |  |
| `wrangler turnstile widget list` | `cfctl turnstile widget list` | ✅ full |  |
| `wrangler turnstile widget update` | `cfctl turnstile widget update` | ✅ full |  |
| `wrangler types` | `cfctl types` | ➖ n/a | derives types from the local workerd version; stub points to wrangler types |
| `wrangler vpc` | `cfctl vpc` | ✅ full |  |
| `wrangler vpc service` | `cfctl vpc service` | ✅ full |  |
| `wrangler vpc service create` | `cfctl vpc service create` | ✅ full |  |
| `wrangler vpc service delete` | `cfctl vpc service delete` | ✅ full |  |
| `wrangler vpc service get` | `cfctl vpc service get` | ✅ full |  |
| `wrangler vpc service list` | `cfctl vpc service list` | ✅ full |  |
| `wrangler vpc service update` | `cfctl vpc service update` | ✅ full |  |
| `wrangler whoami` | `cfctl whoami` | ✅ full |  |
| `wrangler workflows` | `cfctl workflows` | ✅ full | + versions list\|get\|graph |
| `wrangler workflows delete` | `cfctl workflows delete` | ✅ full |  |
| `wrangler workflows describe` | `cfctl workflows describe` | ✅ full |  |
| `wrangler workflows instances` | `cfctl workflows instances` | ✅ full |  |
| `wrangler workflows instances delete` | `cfctl workflows instances delete` | ✅ full |  |
| `wrangler workflows instances describe` | `cfctl workflows instances describe` | ✅ full | steps, attempts, errors; "latest" |
| `wrangler workflows instances list` | `cfctl workflows instances list` | ✅ full |  |
| `wrangler workflows instances pause` | `cfctl workflows instances pause` | ✅ full |  |
| `wrangler workflows instances restart` | `cfctl workflows instances restart` | ✅ full |  |
| `wrangler workflows instances resume` | `cfctl workflows instances resume` | ✅ full |  |
| `wrangler workflows instances send-event` | `cfctl workflows instances send-event` | ✅ full |  |
| `wrangler workflows instances terminate` | `cfctl workflows instances terminate` | ✅ full |  |
| `wrangler workflows list` | `cfctl workflows list` | ✅ full |  |
| `wrangler workflows trigger` | `cfctl workflows trigger` | ✅ full |  |
<!-- END GENERATED TABLES -->
