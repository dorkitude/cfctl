# ROADMAP

## Current State (v0.1.0)

- CLI: auth (login/logout/status/setup), whoami, domains (list/get/autorenew),
  zones (list/get/file), records (list/get/create/update/delete)
- Global `--json`, `--account`, `--no-color`
- Tests against a fake Cloudflare API

## Principles

- Stay a sibling of [simple](https://github.com/dorkitude/simple): same verbs,
  flags, and output style, adapted only where Cloudflare's model differs
- High-leverage, everyday operations first
- Never print or persist the token anywhere but its 0600 file

## Phase 1: Parity with simple

- TUI (Bubble Tea) mirroring simple's: tabs, domain dashboard, guarded mutations
- `cfctl demo`: the TUI against a seeded in-memory backend
- `accounts list` and an account switch command
- Interactive installer / Homebrew tap (once the repo is public)

## Phase 2: DNS power features

- Structured record types that need `data` (SRV, CAA, HTTPS/SVCB, TLSA, ...)
- Record tags and comments filters
- Batch record changes (`POST /zones/{id}/dns_records/batch`) with a preview diff
- Zone file import (`zones import`)
- DNSSEC status / enable / disable and DS record display
- `records list --proxied` / `--content` filters

## Phase 3: Registrar lifecycle

- Domain lock and WHOIS privacy toggles
- Domain search / availability check (`registrar domain-check`)
- Registration (billable; needs strong confirmation) and transfer-in status
- Registrant contact view/update
- Expiry report (`domains list --expiring 60d`)

## Phase 4: Zone operations

- Zone create / delete / pause / unpause
- Zone settings (SSL mode, always HTTPS, min TLS) get/set
- Cache purge (by URL, tag, everything)
- Page rules / redirect rules basics

## Tooling

- Shell completions
- Coverage matrix against the Cloudflare OpenAPI spec for the resources above
