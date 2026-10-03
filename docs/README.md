# cfctl documentation

The [top-level README](../README.md) is the quick tour. These pages are the
full reference. Every page is also embedded in the binary, so it works
offline: `cfctl docs <topic>` renders it in the terminal (`cfctl docs --list`
shows the topics).

## Start here

| Page | What's in it | `cfctl docs` topic |
|---|---|---|
| [../README.md](../README.md) | Install, log in, quick start for every area | `readme` |
| [cfctl-vs-wrangler.md](cfctl-vs-wrangler.md) | Quick matrix, all 361 wrangler commands mapped, when to still use wrangler, migration cheatsheet | `cfctl-vs-wrangler` |
| [api.md](api.md) | The generated tree for all 3,645 API operations, discovery, raw requests, GraphQL, pagination, the read-only guard | `api` |

## Command reference

| Page | Commands | `cfctl docs` topic |
|---|---|---|
| [commands/core.md](commands/core.md) | `auth`, `whoami`, `domains`, `zones`, `records`; tokens, permissions, config files | `core` |
| [commands/workers.md](commands/workers.md) | `workers`, `deploy`, `versions`, `deployments`, `rollback`, `secret`, `triggers`, `tail`, `dispatch-namespace`, `preview` | `workers` |
| [commands/storage.md](commands/storage.md) | `kv`, `r2`, `d1`, `queues`, `hyperdrive`, `vectorize`, `secrets-store`, `k2`, `basin`, `artifacts`, `agent-memory` | `storage` |
| [commands/platform.md](commands/platform.md) | `pages`, `ai`, `ai-search`, `workflows`, `containers`, `browser`, `flagship`, `email`, `turnstile`, `tunnel`, `vpc`, `cert`, `mtls-certificate`, `docs`, `completion`, `init` | `platform` |
| [commands/admin.md](commands/admin.md) | `zones settings`, `cache`, `ssl`, `rulesets`, `redirects`, `transform`, `waf`, `page-rules`, `firewall`, `lists`, `lb`, `analytics`, `accounts`, `members`, `roles`, `tokens`, `audit-logs`, `billing`, `logpush`, `notifications`, `healthchecks`, `waiting-rooms`, `spectrum`, `access`, `user`, `dns` | `admin` |

## Reference data

- [wrangler-map/](wrangler-map/): one TSV per area (`workers`, `storage`,
  `platform`) with every wrangler command, its cfctl equivalent, a status
  (`full`, `partial`, `not-applicable`), and notes.

## For contributors

| Page | What's in it | `cfctl docs` topic |
|---|---|---|
| [ARCHITECTURE.md](ARCHITECTURE.md) | Package layout, request layer, read-only guard, generated commands, adding a command group, versioning | `architecture` |
| [write-paths.md](write-paths.md) | Every write command, the endpoints it calls, and how it was checked against Cloudflare's API | `write-paths` |
| [smoke/README.md](smoke/README.md) | Read-only smoke tests against a live account, and the latest results | |
| [../CONTRIBUTING.md](../CONTRIBUTING.md) | Dev setup, Make targets, testing rules, release checklist | — |
| [../AGENTS.md](../AGENTS.md) | Rules for coding agents working on cfctl | — |
| [../CHANGELOG.md](../CHANGELOG.md) | What changed in each version | `changelog` |
| [../ROADMAP.md](../ROADMAP.md) | What's next | `roadmap` |
