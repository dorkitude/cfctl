# ☁️ cfctl

The whole Cloudflare platform from your terminal, built with Go and Cobra in
the style of [linctl](https://github.com/dorkitude/linctl): Workers, KV, R2,
D1 and the rest of the storage products, Pages, Workers AI, Tunnels, zone
settings, SSL, cache, WAF, analytics, accounts and API tokens, Registrar
domains and DNS. Plus a generated command for **every one of the 3,645
operations** in Cloudflare's API, and raw JSON / GraphQL escape hatches for
anything else.

**Is it a superset of wrangler?** For everything wrangler does *to your
Cloudflare account*, yes: every API-backed wrangler command has a cfctl
equivalent (321 of 361 fully, 24 partially), and cfctl adds everything wrangler
can't reach (Registrar, DNS, zone settings, SSL, cache, WAF, API tokens, and the
other 3,000+ API operations). It is **not** a replacement for wrangler's *local
tooling*: the `wrangler dev` / `pages dev` local runtime, `types`, built-in
bundling (cfctl deploys pre-built scripts, or bundles with `esbuild` if it's on
your `PATH`), Pages Functions builds, and D1 `--local`. Keep wrangler for local
development; use cfctl for everything that talks to Cloudflare.

Everyday wrangler tasks in cfctl:

| Task | wrangler | cfctl |
|---|---|---|
| Deploy a Worker | `wrangler deploy` | `cfctl deploy` |
| Gradual rollout / rollback | `wrangler versions deploy`, `wrangler rollback` | `cfctl versions deploy <id>@10 <id>`, `cfctl rollback` |
| Live logs | `wrangler tail` | `cfctl tail my-worker` |
| Secrets | `wrangler secret put API_KEY` | `cfctl secret put API_KEY --name my-worker` |
| KV | `wrangler kv key put` | `cfctl kv key put my-cache greeting hello` |
| R2 | `wrangler r2 object put` | `cfctl r2 put my-bucket/img/logo.png --file logo.png` |
| D1 | `wrangler d1 execute` | `cfctl d1 execute my-db --command "SELECT 1"` |
| Pages | `wrangler pages deploy` | `cfctl pages deploy ./dist --project-name my-site` |
| Queues | `wrangler queues create` | `cfctl queues create jobs` |
| Workers AI | `wrangler ai models` | `cfctl ai models list` |
| Local dev server | `wrangler dev` | not covered: use wrangler |

➡️ **[cfctl vs wrangler](docs/cfctl-vs-wrangler.md)**: the quick matrix, all
361 wrangler commands mapped (321 full, 24 partial, 16 not applicable), and a
migration cheatsheet.

## ✨ Features

- **Every API operation**: `cfctl api <tag> <op>`, generated from Cloudflare's
  OpenAPI spec, with `api search` / `describe` discovery.
- **Raw escape hatches**: `cfctl api request` (any method, any path) and
  `cfctl graphql`.
- **Workers**: deploy from `wrangler.toml`/`.json(c)`, versions and gradual
  rollouts, rollback, secrets, triggers, routes, custom domains, live tail,
  stored logs, dispatch namespaces, Previews.
- **Storage**: KV, R2 (plus `ls`, `du`, `usage` with cost, `sync`), D1 (query,
  export/import, Time Travel, migrations), Queues, Hyperdrive, Vectorize,
  Secrets Store, K2, Basin, Artifacts, Agent Memory.
- **Platform**: Pages, Workers AI, AI Search, Workflows, Containers, Browser
  Run, Flagship, Email, Turnstile, Tunnels, Workers VPC, mTLS certificates.
- **Zone & account admin**: settings, cache, SSL/TLS, rulesets, redirects,
  transforms, WAF, firewall, lists, load balancing, analytics, accounts,
  members, roles, API tokens, audit logs, billing, Logpush, notifications,
  health checks, waiting rooms, Spectrum, Access.
- **DNS**: zones list/get/export, records list/get/create/update/delete,
  DNSSEC, zone file import.
- **Registrar**: list domains with expiry, toggle auto-renew.
- **Authentication**: API token login (user tokens or `cfat_` account tokens),
  account ID first, hidden prompt or piped stdin, verified before saving.
- **Read-only guard**: `--read-only` / `CFCTL_READONLY=1` refuses every
  write, for every command, before it's sent.
- **Output modes**: styled tables, `--json`, `--no-color`.
- **Built-in docs**: `cfctl docs`, offline.
- **Never prints your token**: not in output, errors, `--json`, or `--debug`.


## 📦 Installation

### Homebrew (macOS/Linux)
```bash
brew install dorkitude/tap/cfctl
cfctl docs                       # render this README
```

### Scoop (Windows)
```powershell
scoop bucket add dorkitude https://github.com/dorkitude/scoop-bucket
scoop install dorkitude/cfctl
cfctl docs
```

### Ubuntu/Debian (.deb) and Fedora/RHEL (.rpm)
Download the package for your architecture from the
[latest release](https://github.com/dorkitude/cfctl/releases/latest), then:
```bash
sudo apt install ./cfctl_*_amd64.deb      # Ubuntu/Debian
sudo dnf install ./cfctl-*.x86_64.rpm     # Fedora/RHEL
```

### Prebuilt binaries
macOS, Linux, and Windows archives (amd64 and arm64) are attached to every
[release](https://github.com/dorkitude/cfctl/releases), with `checksums.txt`.

### Go install
```bash
go install github.com/dorkitude/cfctl@latest
```

### From source

```bash
git clone https://github.com/dorkitude/cfctl.git
cd cfctl
make build                       # ./cfctl
make install                     # go install into $(go env GOPATH)/bin
cfctl docs
```

### For development

```bash
git clone https://github.com/dorkitude/cfctl.git
cd cfctl
go run . --help                  # run without building
make test                        # fake-API tests, no token or network needed
```

See [CONTRIBUTING.md](CONTRIBUTING.md). Requires Go `1.26+`.

## ⚠️ Important

**Use the read-only guard when exploring a real account.** With
`--read-only` or `CFCTL_READONLY=1`, cfctl refuses every request except
GET/HEAD/OPTIONS and GraphQL queries. The check lives in the HTTP transport
every command shares (the Cloudflare SDK included), so nothing can bypass it,
and a refused request is never sent.

```bash
export CFCTL_READONLY=1
cfctl api request POST /accounts/{account_id}/r2/buckets --data '{"name":"x"}'
# ✗ refusing POST /client/v4/accounts/.../r2/buckets: read-only mode
```

**Destructive commands ask first.** On a terminal you get a prompt; in scripts
pass `--yes` (`-y`). Without a terminal and without `--yes`, they refuse.

**`auth login` asks for your account ID before the token.** It's 32 hex
characters, on the dashboard account home, in the dashboard URL, or from
`wrangler whoami`. Piped logins need `--account <id>`.

## 🚀 Quick Start

> **IMPORTANT** Agents like Claude Code, Cursor, and Codex should use the
> `--json` flag on all read operations, and `CFCTL_READONLY=1` unless they
> mean to change something.

### 1. Authentication
```bash
# How to create the token, and which permissions it needs
cfctl auth setup

# Log in: account ID first, then the token (hidden input)
cfctl auth login

# Or pipe the token in (needs --account)
printf '%s' "$CLOUDFLARE_API_TOKEN" | cfctl auth login --account <account-id>

# Check where the token comes from, and verify it live
cfctl auth status --verify

# Show the token, account, and user
cfctl whoami
```

### 2. Workers
```bash
# Browse
cfctl workers list
cfctl workers get my-worker

# Deploy from ./wrangler.toml (or .json/.jsonc); see exactly what would be sent first
cfctl deploy --dry-run
cfctl deploy
cfctl deploy --env staging

# Gradual rollout, then roll back
cfctl versions upload
cfctl versions deploy <new-version>@10 <old-version>
cfctl rollback

# Secrets (hidden prompt or stdin, never argv)
cfctl secret put API_KEY --name my-worker
cfctl secret list --name my-worker

# Live logs and stored logs
cfctl tail my-worker --status error
cfctl workers logs my-worker
```

### 3. Storage
```bash
# KV (namespaces by title or ID)
cfctl kv namespace list
cfctl kv key put CACHE greeting hello --ttl 3600
cfctl kv key get CACHE greeting

# R2
cfctl r2 buckets list
cfctl r2 ls media/images/
cfctl r2 put media/logo.png --file logo.png
cfctl r2 du media --by ext
cfctl r2 usage                             # objects, size, operations, and a cost estimate

# D1 (remote)
cfctl d1 list
cfctl d1 execute my-db --command "SELECT count(*) FROM users"
cfctl d1 migrations apply my-db

# Queues, Hyperdrive, Vectorize
cfctl queues list
cfctl hyperdrive list
cfctl vectorize list
```

### 4. Platform
```bash
# Pages (Direct Upload)
cfctl pages project list
cfctl pages deploy ./dist --project-name my-site

# Workers AI
cfctl ai models list
cfctl ai run @cf/meta/llama-3.1-8b-instruct --prompt "Say hi"

# Workflows, Tunnels, Turnstile, Email Routing
cfctl workflows list
cfctl tunnel list
cfctl turnstile widget list
cfctl email routing status example.com
```

### 5. Zone & account admin
```bash
# Zone settings, SSL, cache
cfctl zones settings get example.com min_tls_version
cfctl ssl status example.com
cfctl ssl mode example.com strict
cfctl cache purge example.com --url https://example.com/app.js

# Rules and security
cfctl redirects list example.com
cfctl waf overview example.com

# Traffic analytics (GraphQL presets)
cfctl analytics zone example.com --since 7d

# Accounts, tokens, audit logs
cfctl accounts list
cfctl tokens list
cfctl audit-logs list --since 7d
```

### 6. Domains & DNS
```bash
# Registrar domains, with expiry and auto-renew (↻)
cfctl domains list
cfctl domains autorenew example.com on

# Zones
cfctl zones list
cfctl zones file example.com               # BIND export

# DNS records (zone by name or ID; names relative, @, or FQDN)
cfctl records list example.com --type A
cfctl records create example.com --type A --name www --content 192.0.2.1 --proxied
cfctl records update example.com <record-id> --proxied=false --ttl 300
cfctl records delete example.com <record-id>

# DNSSEC
cfctl dns dnssec status example.com
```

### 7. Every API operation
```bash
# Find an operation
cfctl api search custom hostname
cfctl api describe custom-hostname-for-a-zone create-custom-hostname

# Call it: path params are arguments, query params are flags, --zone fills {zone_id}
cfctl api custom-hostname-for-a-zone list-custom-hostnames --zone example.com
cfctl api zone list-zones --per-page 50 --all
cfctl api dns-records-for-a-zone create-dns-record --zone example.com \
    --data '{"type":"A","name":"www","content":"192.0.2.1","ttl":1}'

# Which spec is embedded
cfctl api spec-info
```

### 8. Raw API & GraphQL (escape hatch)
```bash
# Any method, any path ({account_id} and {zone_id} are filled in)
cfctl api request GET /accounts/{account_id}/r2/buckets
cfctl api request GET /zones/{zone_id}/dns_records --zone example.com --query type=A --all
cfctl api request POST /accounts/{account_id}/r2/buckets --data '{"name":"x"}'
cfctl api request GET /user/tokens/verify --raw          # whole envelope

# GraphQL Analytics: positional, --file, or stdin; with variables
cfctl graphql 'query { viewer { accounts(filter:{accountTag:"{account_id}"}) {
  r2OperationsAdaptiveGroups(limit:10, filter:{datetime_geq:"2026-10-01T00:00:00Z"}) {
    sum { requests } dimensions { actionType bucketName } } } } }'
cfctl graphql --file query.graphql --variables '{"zone":"{zone_id}"}' --zone example.com
cat query.graphql | cfctl graphql --variables-file vars.json
```

Full guide: [docs/api.md](docs/api.md) (discovery, parameters, pagination
styles, the read-only guard).

## 🆚 cfctl vs wrangler

| Area | wrangler | cfctl |
|---|:---:|:---:|
| Deploy, versions, rollback, secrets, triggers | ✅ | ✅ (deploy 🟡: no built-in bundler) |
| Live tail | ✅ | 🟡 |
| KV, R2, Queues, Hyperdrive, Vectorize | ✅ | ✅ |
| D1 | ✅ | 🟡 remote only |
| Pages | ✅ | 🟡 no Functions bundling |
| Local dev server (`dev`), `types` | ✅ | n/a |
| Registrar, DNS records, zone settings, SSL, cache, WAF | ❌ | ✅ |
| API tokens, audit logs, analytics, load balancing | ❌ | ✅ |
| Every API operation, raw JSON, GraphQL | ❌ | ✅ |
| Read-only guard | ❌ | ✅ |

The full matrix, per-command tables, and a migration cheatsheet are in
**[docs/cfctl-vs-wrangler.md](docs/cfctl-vs-wrangler.md)**.

## 🧭 Command Reference

### Global flags

- `--json`: JSON output for scripting
- `--account <id>`: Cloudflare account ID (overrides the cached one)
- `--no-color`: disable colors (also honors `NO_COLOR`)
- `--debug`: log each API request (method, path, status, duration) to stderr;
  never headers, bodies, or the token (also `CFCTL_DEBUG=1`)
- `--read-only`: refuse every request that could change anything (also
  `CFCTL_READONLY=1`)
- `--timeout <duration>`: timeout per API request attempt (default `30s`)

Not global, but shared: `--zone <name-or-id>` (also `CFCTL_ZONE`) fills
`{zone_id}` for `api` and `graphql`, and switches commands that work at both
levels (`rulesets`, `logpush`, `access`, ...) to the zone. `--yes` / `-y`
skips confirmations on destructive commands.
- `--help`, `-h` / `--version`, `-v`

### Command groups

`cfctl --help` lists every command, grouped by area. Each area has a full
reference page:

| Area | Commands | Reference |
|---|---|---|
| Core | `auth`, `whoami`, `domains`, `zones`, `records`, `docs`, `completion` | [core.md](docs/commands/core.md) |
| Workers | `workers`, `deploy`, `versions`, `deployments`, `rollback`, `secret`, `triggers`, `tail`, `dispatch-namespace`, `preview`, `init` | [workers.md](docs/commands/workers.md) |
| Storage | `kv`, `r2`, `d1`, `queues`, `hyperdrive`, `vectorize`, `secrets-store`, `k2`, `basin`, `artifacts`, `agent-memory` | [storage.md](docs/commands/storage.md) |
| Platform | `pages`, `ai`, `ai-search`, `workflows`, `containers`, `browser`, `flagship`, `email`, `turnstile`, `tunnel`, `vpc`, `cert`, `mtls-certificate` | [platform.md](docs/commands/platform.md) |
| Zone & account | `dns`, `cache`, `ssl`, `rulesets`, `redirects`, `transform`, `waf`, `page-rules`, `firewall`, `lists`, `lb`, `analytics`, `accounts`, `members`, `roles`, `tokens`, `audit-logs`, `billing`, `logpush`, `notifications`, `healthchecks`, `waiting-rooms`, `spectrum`, `access`, `user` | [admin.md](docs/commands/admin.md) |
| API & raw | `api` (3,645 generated operations, `request`, `search`, `describe`, ...), `graphql` | [api.md](docs/api.md) |

Index of all docs: [docs/README.md](docs/README.md).

## 🖨️ Output Formats

- **Styled (default)**: tables and summaries with colors and icons (`↻`
  auto-renew, `☁ proxied`, `▶` current deployment).
- **JSON (`--json`)**: the API's own objects (or a documented cfctl shape for
  computed output such as `r2 du` or `analytics`). The generated `cfctl api`
  commands always print JSON: the response's `result`, or the whole envelope
  with `--raw`.
- **No color (`--no-color` / `NO_COLOR`)**: the same layout without colors.
  Output piped to another program has no colors either.

Raw bodies (KV values, R2 objects, exports, zone files) go to stdout as-is,
so they can be redirected.

## 🔑 Tokens, permissions, and config

- **User tokens** (My Profile → API Tokens) and **account-owned tokens**
  (Manage Account → Account API Tokens, prefixed `cfat_`) both work. The type
  is shown by `whoami` and `auth status`. A few endpoints (`cfctl user`,
  legacy Page Rules) need a user token.
- Minimum permissions for the core commands: Account Settings: Read, Domain
  Registration: Edit, Zone: Edit, DNS: Edit. Other areas need their product's
  permission; `cfctl auth setup` walks through it and a 403 names the likely
  missing one.
- Storage: `~/.config/cfctl/token` (0600) and `~/.config/cfctl/config.yaml`
  (account ID, token type; never the token). Overrides: `CFCTL_TOKEN` /
  `CLOUDFLARE_API_TOKEN`, `CFCTL_ACCOUNT_ID`, `CFCTL_CONFIG_DIR`.

Details: [docs/commands/core.md](docs/commands/core.md#authentication).

## 🤖 Scripting & Automation

```bash
# Domains expiring, with auto-renew status
cfctl domains list --json | jq '.[] | {domain_name, expires_at, auto_renew}'

# Every A record in a zone
cfctl records list example.com --json | jq -r '.[] | select(.type=="A") | "\(.name) \(.content)"'

# Worker names
cfctl workers list --json | jq -r '.[].id'

# Secrets from a password manager, never on the command line
pass show cloudflare/api-key | cfctl secret put API_KEY --name my-worker

# Non-interactive deletes need --yes
cfctl records delete example.com <record-id> --yes

# Guaranteed read-only report
CFCTL_READONLY=1 cfctl r2 usage --json
```

## 🛠 Troubleshooting

### Authentication
```bash
cfctl auth status --verify       # token source, type, and a live check
cfctl auth login --force         # replace the saved token
cfctl --debug whoami             # see each request's method, path, and status
```

### Common errors
- **`HTTP 403` / "Authentication error"**: the token lacks a permission for
  that product. The error names the likely one; edit the token in the
  dashboard and add it.
- **Errors on `/user` endpoints with a `cfat_` token**: account tokens can't
  read `/user`. Use a user token for `cfctl user`.
- **"refusing ... read-only mode"**: `--read-only` or `CFCTL_READONLY=1` is
  set. Some reads are POSTs at Cloudflare (D1 queries, AI inference), so they
  are refused too. Unset it to make changes.
- **"needs --yes"**: a destructive command ran without a terminal.
- **Zone not found**: zones are matched by exact name or 32-hex ID; check
  `cfctl zones list` and `--account`.
- **Several accounts**: pass `--account <id>` or set `CFCTL_ACCOUNT_ID`.
- **Requests hang or time out**: lower or raise `--timeout` (default `30s`);
  429s and 5xx on safe requests are retried with backoff.

### Rate limits
Cloudflare allows 1,200 API requests per 5 minutes per user. Large `--all`
listings and `r2 du` on big buckets count against it; `--max-pages` bounds
`--all`.

## 📚 Built-in docs

```bash
cfctl docs                       # this README
cfctl docs --list                # every topic
cfctl docs cfctl-vs-wrangler     # the wrangler comparison
cfctl docs api                   # the generated API tree
cfctl docs workers               # one command reference
cfctl docs --search tunnel       # which topics mention a word
cfctl docs readme --raw > cfctl.md
```

The docs are embedded in the binary, so they match your version and work
offline.

## 🔢 Versioning

cfctl uses semver with large patch numbers so small changes have room
(`0.2.710`, `0.2.711`, ...); each release is tagged with its version
(`v0.2.710`). Changes are recorded in [CHANGELOG.md](CHANGELOG.md).

## 🤝 Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup, Make targets, testing rules,
and the release checklist, and [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
for how the pieces fit together. Coding agents: read [AGENTS.md](AGENTS.md).
Security reports: see [SECURITY.md](SECURITY.md).

## 📄 License

MIT. See [LICENSE](LICENSE).

cfctl bundles Cloudflare's OpenAPI schema
([cloudflare/api-schemas](https://github.com/cloudflare/api-schemas)), which is
BSD-3-Clause licensed; see
[internal/apispec/LICENSE-cloudflare-api-schemas](internal/apispec/LICENSE-cloudflare-api-schemas).

cfctl is an independent project. It is not affiliated with or endorsed by
Cloudflare, Inc.

## 🔗 Links

- [Cloudflare API reference](https://developers.cloudflare.com/api/)
- [Cloudflare OpenAPI schemas](https://github.com/cloudflare/api-schemas)
- [wrangler](https://developers.cloudflare.com/workers/wrangler/)
- [linctl](https://github.com/dorkitude/linctl), the Linear CLI whose design cfctl follows
