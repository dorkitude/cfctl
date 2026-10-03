# The API tree: every Cloudflare operation

`cfctl api` reaches the whole Cloudflare API v4. It has two halves:

- **Generated commands**: `cfctl api <tag> <operation>`, one command for each
  of the **3,645 operations** in Cloudflare's official OpenAPI spec, grouped
  into 575 groups (one per OpenAPI tag).
- **Raw requests**: `cfctl api request <METHOD> <PATH>`, any path, any body.

Plus `cfctl graphql` for the GraphQL Analytics API. All three use your stored
token, fill in `{account_id}` / `{zone_id}`, follow pagination with `--all`,
and go through the same read-only guard as every other cfctl command.

Use the hand-written groups (`cfctl r2`, `cfctl workers`, `cfctl ssl`, ...)
when one exists: they have friendly names, tables, and confirmations. Reach for
`cfctl api` when they don't cover something, or when you want the API's exact
shapes.

> **Agents:** the generated commands always print JSON (the response's
> `result`), so `--json` is implied. Use `cfctl api describe` before sending a
> body, and run with `CFCTL_READONLY=1` while exploring.

- [Find an operation](#find-an-operation)
- [Call a generated command](#call-a-generated-command)
- [Worked example: search, describe, call](#worked-example-search-describe-call)
- [Raw requests](#raw-requests)
- [GraphQL Analytics](#graphql-analytics)
- [Pagination](#pagination)
- [Read-only guard](#read-only-guard)
- [Output](#output)
- [The embedded spec](#the-embedded-spec)

## Find an operation

```bash
cfctl api tags                          # every group, with its operation count
cfctl api search r2 bucket              # every word must match (case-insensitive)
cfctl api search dns_records            # matches operationId, summary, path, tag, or command name
cfctl api search workers script delete
cfctl api list --tag r2-bucket          # every operation in one group
cfctl api list --method delete          # every DELETE in the API
cfctl api list --json                   # the whole table, for jq
cfctl api r2-bucket --help              # a group's commands
cfctl api describe dns-records-for-a-zone create-dns-record   # params, body schema, example body
cfctl api describe dns-records-for-a-zone-create-dns-record   # by operationId works too
```

`search` prints one line per match: the command (`<tag> <operation>`), the
method and path, and the summary. `describe` shows the path, the operationId,
the exact cfctl command line, every path/query/header parameter (type, enum,
default, required), the request body schema with `$ref`s resolved, and an
example skeleton you can paste into `--data`.

Names are derived from the spec: the group is the kebab-cased first OpenAPI tag
(`DNS Records for a Zone` → `dns-records-for-a-zone`), the operation is the
kebab-cased summary (`Create DNS Record` → `create-dns-record`). Shell
completion covers the generated tree too (`cfctl completion --help`).

## Call a generated command

| Spec element | On the command line |
|---|---|
| `{account_id}` | From your config, `--account`, or `CFCTL_ACCOUNT_ID` |
| `{zone_id}` / `{zone_identifier}` | `--zone <name-or-id>` (or `CFCTL_ZONE`) |
| Any other path parameter | A positional argument, in path order |
| Query parameter `per_page` | A flag: `--per-page 50` (enums are validated) |
| Query parameter that clashes with a cfctl flag | `--param-<name>`, e.g. `--param-query` |
| Query parameter that can't be a flag (`meta.<field>[op]`) | `--query 'k=v'` (repeatable; bypasses validation) |
| Header parameter | `--header 'Name: value'` |
| JSON body | `--data '<json>'`, `--data @file.json`, or `--data -` (stdin) |
| Multipart body | `--form name=value`, `--form 'file=@path;type=mime'` (repeatable) |

```bash
cfctl api zone list-zones --per-page 50 --all
cfctl api zone list-zones --name example.com --status active
cfctl api r2-bucket list-buckets
cfctl api r2-bucket get-bucket my-bucket                  # path param as an argument
cfctl api dns-records-for-a-zone list-dns-records --zone example.com --type A
cfctl api dns-records-for-a-zone create-dns-record --zone example.com \
    --data '{"type":"A","name":"www","content":"192.0.2.1","ttl":1}'
cfctl api worker-script list-worker-scripts
cfctl api d1 list-d1-databases
cfctl api r2-bucket delete-bucket old-bucket --yes        # DELETEs ask first; --yes skips
```

- Required parameters and required bodies are enforced before anything is
  sent.
- `DELETE` operations (and the ones the spec marks as needing confirmation)
  ask on a terminal; without one they need `--yes`. In read-only mode they
  don't ask: the guard refuses them.
- `--raw` prints the whole response envelope instead of `result`.

## Worked example: search, describe, call

Say you want to add a custom hostname (Cloudflare for SaaS), which no
hand-written group covers.

```bash
# 1. Find the operation
cfctl api search custom hostname
# custom-hostname-for-a-zone list-custom-hostnames    GET  /zones/{zone_id}/custom_hostnames   List Custom Hostnames
# custom-hostname-for-a-zone create-custom-hostname   POST /zones/{zone_id}/custom_hostnames   Create Custom Hostname
# ...

# 2. See its parameters and an example body
cfctl api describe custom-hostname-for-a-zone create-custom-hostname

# 3. Look before you leap: the read-only guard lets the list through...
CFCTL_READONLY=1 cfctl api custom-hostname-for-a-zone list-custom-hostnames --zone example.com

# 4. ...then create it
cfctl api custom-hostname-for-a-zone create-custom-hostname --zone example.com \
    --data '{"hostname":"app.customer.com","ssl":{"method":"http","type":"dv"}}'
```

## Raw requests

`cfctl api request` sends any method to any path under
`https://api.cloudflare.com/client/v4`, with the same placeholders, body
flags, pagination, and output as the generated commands. Use it for endpoints
missing from the spec, or to paste a path straight from Cloudflare's docs.

```bash
cfctl api request GET /zones --all
cfctl api request GET /accounts/{account_id}/r2/buckets
cfctl api request GET /zones/{zone_id}/dns_records --zone example.com --query type=A
cfctl api request POST /zones/{zone_id}/dns_records --zone example.com \
    --data '{"type":"A","name":"www","content":"192.0.2.1","ttl":1}'
echo '{"name":"x"}' | cfctl api request POST /accounts/{account_id}/r2/buckets --data -
cfctl api request PUT /accounts/{account_id}/workers/scripts/hello \
    --form 'metadata={"main_module":"index.js"};type=application/json' \
    --form 'index.js=@index.js;type=application/javascript+module'
cfctl api request GET /user/tokens/verify --raw         # the whole envelope
```

- Placeholders: `{account_id}`, `{zone_id}` (and `{zone_identifier}`). Values
  are path-escaped; an unknown placeholder is an error.
- `--query k=v` and `--header 'Name: value'` are repeatable.
- Non-JSON responses (KV values, exports, zone files) are written as-is, so
  you can redirect them to a file.

## GraphQL Analytics

`cfctl graphql` runs a query against `https://api.cloudflare.com/client/v4/graphql`.
Use it for analytics the REST API doesn't expose (R2 operations and storage,
Workers invocations, HTTP requests, firewall events, ...). For the common
cases, `cfctl analytics` has presets (see [admin.md](commands/admin.md#analytics-graphql-presets)).

```bash
# Positional query
cfctl graphql 'query { viewer { accounts(filter:{accountTag:"{account_id}"}) {
  r2OperationsAdaptiveGroups(limit:10, filter:{datetime_geq:"2026-10-01T00:00:00Z"}) {
    sum { requests } dimensions { actionType bucketName } } } } }'

# From a file, with variables (placeholders are filled in string variables too)
cfctl graphql --file query.graphql --variables '{"zone":"{zone_id}"}' --zone example.com
cfctl graphql --file query.graphql --variables-file vars.json

# From stdin
cat query.graphql | cfctl graphql --variables '{"acct":"{account_id}","since":"2026-10-01"}'
```

The response (`{"data": ..., "errors": ...}`) is printed as JSON, and the
command exits non-zero if `errors` is set.

## Pagination

Without `--all`, you get the one page the API returns. With `--all`, cfctl
follows the pages and prints the concatenated results as one JSON array (or
merged object). It picks the style from the operation's parameters in the
spec (for `api request` too: the path is matched against the spec), falling
back to what the response carries:

| Style | Request parameters | How cfctl advances |
|---|---|---|
| **page** | `page` / `page_no` + `per_page` / `page_size` / `pageSize` / `perPage` | Next page while `total_pages`, `total_count`, `has_more`, or a full page says there is more |
| **cursor** | `cursor`, `page_token`, `continuation_token`, `next_cursor`, `scan_cursor` | The next token from the response (`result_info.cursor`, `cursors.after`, `next_cursor`, `next_page_token`, ...) until there is none or `has_more` / `is_truncated` is false |
| **offset** | `offset` + `limit` | `offset +=` items received, while `total` or a full page says there is more |
| **before** | `before` (time window, newest first) | `before` = the last item's creation time while pages are full; only when the endpoint has no other paging (Stream videos) |

Lists wrapped in an object (`{"buckets": [...]}`) or returned at the top
level of the body (`{"data": [...], "paging": {...}}`) are merged too.

The loop always ends: on the last page, an empty page, a repeated cursor, a
page identical to the previous one, or after `--max-pages N` requests (default
1000; cfctl warns when it stops early). Hand-written list commands page for you (no flag needed), and some take
`--page` / `--per-page` to fetch one page instead.

```bash
cfctl api zone list-zones --all --per-page 50
cfctl api request GET /accounts/{account_id}/workers/scripts --all
cfctl api request GET /accounts/{account_id}/storage/kv/namespaces --all --max-pages 5
```

## Read-only guard

`--read-only` (or `CFCTL_READONLY=1`) makes cfctl refuse every request except
`GET`, `HEAD`, `OPTIONS`, and `POST /graphql` (Cloudflare's GraphQL API is
analytics, so read-only). The check lives in the HTTP transport that every
client shares, the Cloudflare SDK included, so **no command can bypass it**,
and refused requests are never sent.

```bash
export CFCTL_READONLY=1
cfctl api r2-bucket list-buckets            # fine
cfctl api request POST /accounts/{account_id}/r2/buckets --data '{"name":"x"}'
# ✗ refusing POST /client/v4/accounts/.../r2/buckets: read-only mode
```

Some reads are POSTs at Cloudflare (D1 queries, Workers AI inference,
registry credentials), so they are refused too. Use the guard whenever you
point a script, an agent, or a test at a real account.

## Output

- Generated commands and `api request` print the decoded `result` as pretty
  JSON; `--raw` prints the envelope (`success`, `errors`, `messages`,
  `result`, `result_info`).
- API errors keep Cloudflare's codes and messages and exit non-zero. The
  token never appears in output, errors, or `--debug` logs.
- `--debug` (or `CFCTL_DEBUG=1`) logs each request's method, path, status,
  and duration to stderr. `--timeout` sets the per-attempt timeout (default
  `30s`). 429s are retried with backoff (honoring `Retry-After`), 5xx only for
  requests that are safe to repeat (GET, HEAD, OPTIONS, PUT, DELETE, GraphQL).

## The embedded spec

```bash
cfctl api spec-info        # source commit, date, operation counts by method
cfctl api spec > cloudflare-openapi.json
```

The spec is vendored from
[cloudflare/api-schemas](https://github.com/cloudflare/api-schemas) and
compiled into the binary, so discovery works offline. Maintainers refresh it
with `make spec` (see [ARCHITECTURE.md](ARCHITECTURE.md#refreshing-the-api-spec)).
