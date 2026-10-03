# Zone & account admin

[Docs index](../README.md) · [Core](core.md) · [Workers](workers.md) · [Storage](storage.md) · [Platform](platform.md) · [Zone & account](admin.md) · [API](../api.md) · [vs wrangler](../cfctl-vs-wrangler.md)

In the terminal: `cfctl docs admin`.

Commands for the parts of Cloudflare that `wrangler` doesn't cover: zone
settings, cache, SSL/TLS, rules (redirects, transforms, WAF), firewall,
lists, load balancing, analytics, accounts and members, API tokens, audit
logs, billing, Logpush, notifications, health checks, waiting rooms,
Spectrum, Zero Trust Access, your user, and DNS extras.

> **Agents:** pass `--json` on every read. Output is the API's own objects
> (or, for `analytics`, a computed summary), never styled text.

## Conventions

- **Zones**: zone-only commands take the zone (name or ID) as their first
  argument: `cfctl ssl status example.com`. Commands that exist at both
  levels (`rulesets`, `firewall access-rules`, `logpush`, `access apps`) work
  at the **account** level by default and at the **zone** level with
  `--zone <zone>` (or `CFCTL_ZONE`).
- **Account**: from `--account`, `CFCTL_ACCOUNT_ID`, `config.yaml`, or the
  only account the token can see.
- **Lists** fetch every page unless `--page` is given (`--per-page` sets the
  page size).
- **Destructive commands** (`delete`, `revoke`, `remove`, `zones pause`,
  `cache purge --everything`, `dns dnssec disable`, `ssl universal
  --disable`) ask first on a terminal; pass `--yes`/`-y` to skip. Without a
  terminal they refuse unless `--yes` is given. In read-only mode they don't
  ask: the guard refuses them before anything is sent.
- **Bodies**: create/update commands that take arbitrary configuration accept
  `--data JSON|@file|-` (merged over any flags). The shape is the API's; see
  `cfctl api describe <tag> <op>`.
- **Errors** keep Cloudflare's message and add a hint for the usual causes:
  missing token permission (403), plan/entitlement limits, products not
  enabled (Access), and `/user` or Page Rules endpoints that need a
  user-owned token.
- **Secrets are never printed**: Origin CA private keys go to `--key-out`
  (0600), new API token values to `--value-out` (0600), Logpush destination
  credentials are shown as `REDACTED`, webhook secrets are dropped, and
  secret-looking fields in audit log snapshots are redacted, in `--json`
  output too.
- **Everything else**: every operation is also available as a generated
  command (`cfctl api <tag> <op>`); the hand-written commands mention their
  generated equivalent where useful.

## Command tree

```text
zones settings list|get|set     zones create|delete|pause|unpause|activation-check
cache purge|settings|dev-mode
ssl status|mode|verification|universal
ssl packs list|get|order|delete
ssl origin list|get|create|revoke
ssl custom list|get|upload|delete
rulesets list|get|phase|versions|delete
rulesets rules add|update|delete
redirects list|add|delete       redirects bulk lists|items|add|rules
transform list|add|delete
waf overview|managed|rate-limits   waf custom list|add|delete
page-rules list|get|delete
firewall access-rules list|create|delete
lists list|get|create|delete|items|items-add|items-remove|operation
lb list|get|create|update|delete
lb pools list|get|health|create|update|delete
lb monitors list|get|create|delete
analytics zone|paths|countries|firewall|workers|r2
accounts list|get     members list|get|remove     roles list|get
tokens list|get|verify|permission-groups|create|delete
audit-logs list
billing subscriptions|zone|profile|history
logpush jobs list|get|create|update|delete     logpush fields
notifications policies list|get|create|delete
notifications destinations|available|history
healthchecks list|get|create|delete
waiting-rooms list|get|status|events|create|delete
spectrum apps list|get|create|delete
access apps|policies|groups|idps list|get     access organization
user get|invites|memberships
dns dnssec status|enable|disable   dns settings get|set   dns export|import
```

## Zone settings and lifecycle

```bash
cfctl zones settings list example.com               # every setting, value, editable
cfctl zones settings list example.com --filter tls  # names containing "tls"
cfctl zones settings get example.com ssl
cfctl zones settings set example.com always_use_https on
cfctl zones settings set example.com min_tls_version 1.2
cfctl zones settings set example.com browser_cache_ttl 14400
cfctl zones settings set example.com minify '{"css":"on","html":"off","js":"on"}'
```

`set` sends the value as JSON when it parses as JSON (numbers, `true`,
objects), else as a string (`on`, `strict`).

```bash
cfctl zones create example.com [--type full|partial|secondary] [--jump-start]
cfctl zones activation-check example.com    # re-check nameservers of a pending zone now
cfctl zones pause example.com               # asks; traffic bypasses the proxy
cfctl zones unpause example.com
cfctl zones delete example.com --yes        # PERMANENT
```

## Cache

```bash
cfctl cache purge example.com --url https://example.com/app.js --url https://example.com/app.css
cfctl cache purge example.com --prefix example.com/assets/
cfctl cache purge example.com --tag product-123,header
cfctl cache purge example.com --host static.example.com
cfctl cache purge example.com --everything --yes
cfctl cache settings example.com     # cache level, TTLs, dev mode, tiered cache, cache reserve
cfctl cache dev-mode example.com on  # or off; no argument shows it
```

One purge kind per call. Tag, host, and prefix purges may need Enterprise.

## SSL/TLS

```bash
cfctl ssl status example.com           # mode, min TLS, TLS 1.3, HTTPS rewrites, Universal SSL, packs, validation
cfctl ssl mode example.com strict      # off | flexible | full | strict
cfctl ssl verification example.com
cfctl ssl universal example.com [--enable | --disable] [--ca lets_encrypt|google|ssl_com]

cfctl ssl packs list example.com [--all-statuses]
cfctl ssl packs get example.com <pack-id>
cfctl ssl packs order example.com --host example.com --host '*.example.com' --ca lets_encrypt --validity 90 --method txt
cfctl ssl packs delete example.com <pack-id>

cfctl ssl origin list example.com
cfctl ssl origin get <cert-id>
cfctl ssl origin create example.com --host example.com --host '*.example.com' \
    --key-out origin.key --cert-out origin.pem [--type origin-rsa|origin-ecc] [--days 5475]
cfctl ssl origin create example.com --csr @origin.csr --cert-out origin.pem
cfctl ssl origin revoke <cert-id>

cfctl ssl custom list|get|delete example.com ...
cfctl ssl custom upload example.com --cert @cert.pem --key @key.pem [--bundle-method ubiquitous]
```

`ssl origin create` generates the key and CSR locally; the key is written
only after Cloudflare accepts the CSR, and never printed. Without `--host`
it covers the zone and its wildcard.

## Rules: rulesets, redirects, transforms, WAF

Phases can be named in full or by alias: `redirect`, `bulk-redirect`,
`url-rewrite`, `request-headers`, `response-headers`, `waf`/`custom`,
`managed`, `rate-limit`, `cache`, `origin`, `config`, `compression`,
`custom-errors`.

```bash
cfctl rulesets list --zone example.com [--kind zone|managed|custom|root] [--phase waf]
cfctl rulesets list                                   # account level
cfctl rulesets get <ruleset-id> --zone example.com
cfctl rulesets phase cache --zone example.com         # the phase's entrypoint and its rules
cfctl rulesets versions <ruleset-id> --zone example.com
cfctl rulesets rules add --zone example.com --phase waf \
    --expression '(cf.threat_score gt 50)' --action managed_challenge --description "Threats"
cfctl rulesets rules add <ruleset-id> --zone example.com --expression '...' --action redirect \
    --action-parameters '{"from_value":{"target_url":{"value":"https://example.com/"},"status_code":301}}'
cfctl rulesets rules update <ruleset-id> <rule-id> --zone example.com --enabled=false
cfctl rulesets rules delete <ruleset-id> <rule-id> --zone example.com
cfctl rulesets delete <ruleset-id> --zone example.com
```

`rules add --phase` creates the phase entrypoint if the zone has none yet.

```bash
cfctl redirects list example.com
cfctl redirects add example.com --from https://example.com/old --to https://example.com/new [--status 301]
cfctl redirects add example.com --from 'https://www.example.com/*' --to 'https://example.com/${1}'
cfctl redirects add example.com --expression '(http.host eq "old.example.com")' --to https://example.com --preserve-query
cfctl redirects delete example.com <rule-id>
cfctl redirects bulk lists                    # account lists of kind "redirect"
cfctl redirects bulk items <list-id>
cfctl redirects bulk add <list-id> --from example.com/a --to https://example.com/b [--status 301] [--subpath-matching]
cfctl redirects bulk rules                    # which lists are enabled

cfctl transform list example.com [--type url-rewrite|request-headers|response-headers]
cfctl transform add example.com --type response-headers --expression 'true' --set-header 'X-Frame-Options: DENY'
cfctl transform add example.com --type request-headers --expression '(http.host eq "api.example.com")' --remove-header X-Debug
cfctl transform add example.com --type url-rewrite --expression '(http.request.uri.path eq "/old")' --path /new
cfctl transform delete example.com <rule-id>

cfctl waf overview example.com           # security level + managed, custom, and rate limiting rules
cfctl waf managed example.com            # deployed managed rulesets, by name
cfctl waf custom list example.com
cfctl waf custom add example.com --expression '(ip.src.country eq "T1")' --action block --description "Block Tor"
cfctl waf custom delete example.com <rule-id>
cfctl waf rate-limits example.com

cfctl page-rules list|get|delete example.com ...   # legacy; needs a user-owned token
```

## Firewall access rules and lists

```bash
cfctl firewall access-rules list [--zone example.com] [--mode block] [--notes abuse]
cfctl firewall access-rules create 192.0.2.1 --mode block --notes "abuse" [--zone example.com]
cfctl firewall access-rules create 198.51.100.0/24 --mode challenge
cfctl firewall access-rules create AS64496 --mode block
cfctl firewall access-rules create T1 --mode block        # country code
cfctl firewall access-rules delete <rule-id> [--zone example.com]
```

The target (ip, ip6, ip_range, asn, country) is detected from the value;
override with `--target`.

```bash
cfctl lists list
cfctl lists get <list-id>
cfctl lists create blocked_ips --kind ip|hostname|asn|redirect [--description ...]
cfctl lists items <list-id>
cfctl lists items-add <list-id> 192.0.2.1 198.51.100.0/24 [--comment abuse]
cfctl lists items-remove <list-id> <item-id> [--item <id>...]
cfctl lists operation <operation-id>     # item changes are async bulk operations
cfctl lists delete <list-id>
```

## Load balancing

```bash
cfctl lb list example.com
cfctl lb get example.com <lb-id>
cfctl lb create example.com --data @lb.json
cfctl lb update example.com <lb-id> --data '{"enabled":false}'
cfctl lb delete example.com <lb-id>
cfctl lb pools list | get <id> | health <id> | create --data @pool.json | update <id> --data ... | delete <id>
cfctl lb monitors list | get <id> | create --data '{"type":"https","path":"/health"}' | delete <id>
```

## Analytics (GraphQL presets)

`--since` takes `30m`, `24h`, `3d`, `7d`, `30d`, `YYYY-MM-DD`, or RFC 3339
(default `24h`); `--until` ends the window (default now). Ranges up to 3
days use hourly data, longer ones daily data. All are reads (GraphQL POSTs
are allowed in read-only mode).

```bash
cfctl analytics zone example.com [--since 7d] [--limit 10]
  # requests, cached %, bandwidth, encrypted %, page views, threats, uniques,
  # status codes, top countries; --json adds an hourly/daily series
cfctl analytics paths example.com --since 7d --limit 20 [--status 404] [--host www.example.com]
cfctl analytics countries example.com --since 30d
cfctl analytics firewall example.com --since 24h      # security events by action/source/country
cfctl analytics workers --since 7d [--script api]     # requests, errors, subrequests, CPU p50/p99
cfctl analytics r2 --since 30d [--bucket media]       # storage, objects, Class A/B ops, egress per bucket
```

Free plans limit some datasets (hourly zone data: 3 days; grouped firewall
events aren't included, so `analytics firewall` aggregates raw events
instead). Cloudflare's error is shown with a hint when a range is too wide.
For anything else use `cfctl graphql`.

## Accounts, members, roles

```bash
cfctl accounts list
cfctl accounts get [<account-id>]
cfctl members list [--status accepted|pending|rejected]
cfctl members get <member-id>
cfctl members remove <member-id>
cfctl roles list
cfctl roles get <role-id>
```

## API tokens

```bash
cfctl tokens list [--user]
cfctl tokens get <token-id> [--user]
cfctl tokens verify                         # the stored token; user- or account-owned
cfctl tokens permission-groups [--filter dns] [--user]
cfctl tokens create --name ci --policies @policies.json --value-out ci.token [--expires 2027-01-01T00:00:00Z] [--user]
cfctl tokens delete <token-id> [--user]
```

Account-owned tokens by default; `--user` uses `/user/tokens` (needs a
user-owned token). `create` writes the value to `--value-out` (0600, refuses
to overwrite) and redacts it from all output.

## Audit logs

```bash
cfctl audit-logs list                                  # last 24h, newest first
cfctl audit-logs list --since 7d --actor kyle@example.com
cfctl audit-logs list --since 30d --action delete --product dns_records --zone example.com
cfctl audit-logs list --since 2026-09-01 --before 2026-09-15 --all --json
cfctl audit-logs list --v1 --since 7d --action token_create
```

Uses the v2 audit log API (`/logs/audit`, cursor-paged); `--v1` uses the
older `/audit_logs`. `--limit` is the page size (max 1000); `--all` follows
pages to the end of the range.

## Billing

```bash
cfctl billing subscriptions        # account subscriptions (R2, Workers Paid, ...)
cfctl billing zone example.com     # a zone's plan (404 on free zones)
cfctl billing profile
cfctl billing history              # needs a user-owned token
```

## Logpush

```bash
cfctl logpush jobs list [--zone example.com]
cfctl logpush jobs get <job-id> [--zone example.com]
cfctl logpush jobs create --zone example.com --name http --dataset http_requests \
    --destination 'r2://logs/{DATE}?account-id=...&access-key-id=...&secret-access-key=...' \
    --fields ClientIP,ClientRequestHost,EdgeResponseStatus [--filter '...'] [--enabled=false]
cfctl logpush jobs update <job-id> --data '{"enabled":false}' [--zone example.com]
cfctl logpush jobs delete <job-id> [--zone example.com]
cfctl logpush fields http_requests --zone example.com
```

Credentials in `destination_conf` are shown as `REDACTED`.

## Notifications

```bash
cfctl notifications policies list | get <id> | create --data @policy.json | delete <id>
cfctl notifications destinations          # webhooks + PagerDuty (secrets hidden)
cfctl notifications available             # alert types you can subscribe to
cfctl notifications history --since 7d
```

## Health checks, waiting rooms, Spectrum

```bash
cfctl healthchecks list|get|create|delete example.com ...
cfctl waiting-rooms list example.com
cfctl waiting-rooms get|status|events|delete example.com <id>
cfctl waiting-rooms create example.com --data @room.json
cfctl spectrum apps list|get|create|delete example.com ...
```

## Zero Trust Access

```bash
cfctl access apps list [--zone example.com]
cfctl access apps get <app-id>
cfctl access policies list | get <id>
cfctl access groups list | get <id>
cfctl access idps list | get <id>
cfctl access organization
```

Read-only basics; changes go through `cfctl api access-...` generated
commands. If Access isn't enabled on the account, the error says so.

## User

```bash
cfctl user get
cfctl user invites
cfctl user memberships
```

These need a user-owned token; account-owned tokens get a hint saying so.

## DNS extras

`cfctl records` handles individual records. `cfctl dns` covers the rest:

```bash
cfctl dns dnssec status example.com       # status, DS record
cfctl dns dnssec enable example.com       # prints the DS record to add at the registrar
cfctl dns dnssec disable example.com --yes
cfctl dns settings get example.com
cfctl dns settings set example.com flatten_all_cnames true
cfctl dns settings set example.com nameservers.type cloudflare.standard
cfctl dns export example.com [--out example.com.zone]
cfctl dns import example.com example.com.zone [--proxied]
```

DNS records themselves are in [core.md](core.md#records).

## `--all` pagination

Hand-written lists here fetch every page by themselves. For the generated
commands and `cfctl api request`, `--all` follows every pagination style in
Cloudflare's API (page numbers, cursors and named tokens, offset/limit, and
Stream's `before` window); see [api.md](../api.md#pagination).

## Beyond wrangler

Nothing on this page has a wrangler equivalent. See the
[Beyond wrangler](../cfctl-vs-wrangler.md#beyond-wrangler) matrix.
