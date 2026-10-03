# ROADMAP

## Current State (0.2.x)

- Core: auth (user and `cfat_` account tokens), whoami, domains
  (list/get/autorenew), zones, records
- Every API-backed wrangler command: 361 wrangler commands mapped, 321 full,
  24 partial, 16 not applicable (see docs/cfctl-vs-wrangler.md)
- Workers, storage (KV, R2, D1, Queues, Hyperdrive, Vectorize, Secrets Store,
  K2, Basin, Artifacts, Agent Memory), platform (Pages, AI, AI Search,
  Workflows, Containers, Browser Run, Flagship, Email, Turnstile, Tunnels,
  VPC, mTLS), and zone/account admin groups
- `cfctl api`: a generated command for each of the 3,645 operations in the
  official OpenAPI spec, `api request`, discovery, `cfctl graphql`
- Global `--json`, `--account`, `--no-color`, `--debug`, `--read-only`, `--timeout`
- Built-in docs (`cfctl docs`), shell completion
- Tests against a fake Cloudflare API; coverage test over every spec operation

## Next

- Close the remaining wrangler gaps that are API-backed: `tail` reconnect and
  `--ip self`, route/domain removal on deploy
- ~~Read-only smoke test across every read command (`make smoke`)~~ (done)
- ~~First public release~~ (`v0.2.710`)

## Principles

- Follow [linctl](https://github.com/dorkitude/linctl)'s design: same verbs,
  flags, output style, and docs, adapted only where Cloudflare's model differs
- High-leverage, everyday operations first
- Never print or persist the token anywhere but its 0600 file

## Phase 1: TUI and polish

- TUI (Bubble Tea): tabs, domain and zone dashboard, guarded mutations
- `cfctl demo`: the TUI against a seeded in-memory backend
- ~~`accounts list`~~ (done); an account switch command
- ~~Homebrew tap, Scoop bucket, .deb/.rpm packages~~ (done in v0.2.710); a hosted apt repository

## Phase 2: DNS power features

- Structured record types that need `data` (SRV, CAA, HTTPS/SVCB, TLSA, ...)
- Record tags and comments filters
- Batch record changes (`POST /zones/{id}/dns_records/batch`) with a preview diff
- ~~Zone file import~~ (done: `dns import`)
- ~~DNSSEC status / enable / disable~~ (done: `dns dnssec`); DS record display
- `records list --proxied` / `--content` filters

## Phase 3: Registrar lifecycle

- Domain lock and WHOIS privacy toggles
- Domain search / availability check (`registrar domain-check`)
- Registration (billable; needs strong confirmation) and transfer-in status
- Registrant contact view/update
- Expiry report (`domains list --expiring 60d`)

## Phase 4: Zone operations

- ~~Zone create / delete / pause / unpause~~ (done: `zones`)
- ~~Zone settings (SSL mode, always HTTPS, min TLS) get/set~~ (done: `zones settings`, `ssl`)
- ~~Cache purge (by URL, tag, everything)~~ (done: `cache purge`)
- ~~Page rules / redirect rules basics~~ (done: `page-rules`, `redirects`)

## Tooling

- ~~Shell completions~~ (done: `cfctl completion`)
- ~~Coverage matrix against the Cloudflare OpenAPI spec~~ (done: `cfctl api`, TestCoverage)
