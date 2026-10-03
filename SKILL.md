---
name: cfctl
description: Use cfctl to read and change Cloudflare from the terminal - Registrar domains, DNS, Workers, KV/R2/D1/Queues and other storage, Pages, AI, zone settings, SSL, cache, WAF, tokens, audit logs, and any of the 3,645 Cloudflare API operations. Prefer --json reads, run with CFCTL_READONLY=1 unless a change is intended, and use command --help or `cfctl api describe` for exact flags.
---

# cfctl Agent Guide

Use this skill when the user wants to inspect or modify Cloudflare through
`cfctl`.

## Quick Rules

- Verify auth first: `cfctl auth status` or `cfctl whoami --json`.
- **Default to read-only**: prefix commands with `CFCTL_READONLY=1` (or pass
  `--read-only`) unless the user asked for a change. Refused writes are never
  sent.
- Use `--json` on every read and parse with `jq`.
- Inspect current state (`get` / `list --json`) before writing.
- Destructive commands need `--yes` when there is no terminal (always, for
  agents). Only pass it when the user asked for that deletion.
- Exact flags: `cfctl <command> <subcommand> --help`. Offline docs:
  `cfctl docs <topic>` (`cfctl docs --list`).
- Never print, echo, or log the API token. Pipe secrets via stdin
  (`printf %s "$V" | cfctl secret put NAME --name worker`), never argv.

## Finding the right command

1. A hand-written group usually exists: `cfctl --help` lists them by area
   (Core, Workers, Storage, Platform, Zone & account, API & raw).
2. If not, search the generated API tree:
   ```bash
   cfctl api search <words...>                 # e.g. cfctl api search custom hostname
   cfctl api describe <tag> <operation>        # params, body schema, example --data
   cfctl api <tag> <operation> [path-args] --zone example.com [--data '{...}']
   ```
3. Last resort: `cfctl api request <METHOD> /path/{account_id}/... [--data ...]`,
   or `cfctl graphql` for analytics.

## High-Impact Gotchas

- `{account_id}` comes from config / `--account`; `{zone_id}` from
  `--zone <name-or-id>`. Hand-written zone commands take the zone as the first
  argument instead.
- D1 queries, Workers AI inference, and some registry calls are POSTs, so
  read-only mode refuses them even when they only read.
- `cfat_` account tokens can't use `/user` endpoints (`cfctl user`).
- Lists in hand-written commands page automatically; generated/raw calls need
  `--all` to follow pagination.
- `cfctl dev`, `types`, `setup`, `pages dev`, `pages functions build` are stubs
  that point to wrangler: they need a local runtime or bundler.
- `cfctl deploy` doesn't bundle unless `--bundle` (esbuild on PATH); use
  `cfctl deploy --dry-run` to see what would be sent.

## Auth + Credential Behavior

- `cfctl auth login` asks for the account ID first, then the token (hidden).
  Piped: `printf '%s' "$TOKEN" | cfctl auth login --account <id>`.
- Stored in `~/.config/cfctl/token` (0600) and `config.yaml` (account ID).
- Precedence: `CFCTL_TOKEN` > `CLOUDFLARE_API_TOKEN` > token file;
  `--account` > `CFCTL_ACCOUNT_ID` > config.

## References

- README.md (quick start), docs/README.md (index)
- docs/api.md (generated tree, raw requests, GraphQL, pagination)
- docs/commands/{core,workers,storage,platform,admin}.md
- docs/cfctl-vs-wrangler.md (what maps to what)
