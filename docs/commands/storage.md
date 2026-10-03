# Storage

[Docs index](../README.md) · [Core](core.md) · [Workers](workers.md) · [Storage](storage.md) · [Platform](platform.md) · [Zone & account](admin.md) · [API](../api.md) · [vs wrangler](../cfctl-vs-wrangler.md)

In the terminal: `cfctl docs storage`.

cfctl's hand-written commands for Cloudflare's storage products: Workers KV,
R2, D1, Queues, Hyperdrive, Vectorize, Secrets Store, K2, Basin (Pipelines
and the Iceberg catalog), Artifacts, and Agent Memory. They cover every
API-backed wrangler command in these areas (see
[`docs/wrangler-map/storage.tsv`](../wrangler-map/storage.tsv) and
[cfctl vs wrangler](../cfctl-vs-wrangler.md)), plus R2
extras wrangler doesn't have: `ls`, `stat`, `du`, `usage` with a cost
estimate, `sync`, temporary credentials, and background jobs.

> **Agents:** pass `--json` on every read. Output is the API's own objects (or
> a documented cfctl shape for computed output such as `r2 du`).

## Conventions

Shared by every group:

- Resources can be named by **name or ID** (KV titles, D1 names, queue names,
  store names, ...). Ambiguous names are an error that lists the IDs.
- `--json` prints the API's objects (or a documented cfctl shape for
  computed output such as `r2 du` and `r2 usage`).
- Create and update commands accept `--data '<json>'` (or `@file`, `-`);
  flags are merged on top of it, so anything the API accepts is reachable.
- Destructive commands ask first. In scripts pass `--yes` / `-y`; without a
  terminal and without `--yes` they refuse.
- `--read-only` / `CFCTL_READONLY=1` lets only GET/HEAD and GraphQL through.
  Note that D1 queries are POSTs, so `d1 execute`, `export` and `migrations`
  don't work in read-only mode, even for SELECTs.
- The generated `cfctl api <tag> <op>` commands remain available for anything
  not covered here.

## Workers KV

```bash
cfctl kv namespace list [--json]
cfctl kv namespace get <namespace>
cfctl kv namespace create <title> [--jurisdiction eu|fedramp|us]   # prints a wrangler binding snippet
cfctl kv namespace rename <namespace> <new-title>
cfctl kv namespace delete <namespace> [--yes]

cfctl kv key list <namespace> [--prefix P] [--limit N] [--json]   # follows the cursor
cfctl kv key get <namespace> <key> [-o file] [--metadata]          # raw value to stdout
cfctl kv key put <namespace> <key> <value|-> [--ttl SECS] [--expiration TIME] [--metadata JSON]
cfctl kv key put <namespace> <key> --path ./file.bin
cfctl kv key delete <namespace> <key> [--yes]

cfctl kv bulk put <namespace> <file.json|->       # [{"key","value","base64","expiration","expiration_ttl","metadata"}], 10,000 per request
cfctl kv bulk get <namespace> <file.json|key...> [--metadata] [--type json]   # 100 per request
cfctl kv bulk delete <namespace> <file.json|-> [--yes]   # key names or objects with "key"
```

`--expiration` takes RFC 3339, `YYYY-MM-DD`, or Unix seconds. Keys with `/`
or other special characters are percent-encoded for you.

## R2

Objects are addressed as `<bucket>/<key>`. `--jurisdiction eu|us|fedramp|fedramp-high`
applies to every r2 command.

### Buckets

```bash
cfctl r2 buckets list [--name-contains S] [--json]
cfctl r2 buckets get <bucket>                  # alias info; adds object count + size (GraphQL)
cfctl r2 buckets create <bucket> [--location weur] [--storage-class InfrequentAccess]
cfctl r2 buckets update <bucket> --storage-class Standard|InfrequentAccess
cfctl r2 buckets delete <bucket> [--force] [--yes]   # --force deletes every object first
```

### Objects

```bash
cfctl r2 ls <bucket>[/prefix] [prefix] [-r] [--limit N] [--bytes] [--json]
cfctl r2 stat <bucket>/<key>                   # size, ETag, dates, storage class, metadata
cfctl r2 get <bucket>/<key> [--file path] [--pipe]
cfctl r2 put <bucket>/<key> [--file path]      # stdin when --file is omitted
     [--content-type T] [--cache-control V] [--content-disposition V] [--content-encoding V]
     [--content-language V] [--meta k=v]... [--storage-class C]
     [--multipart-threshold MB] [--part-size MiB] [--concurrency N] [--s3]
cfctl r2 rm <bucket>/<key>... [-r] [--dry-run] [--yes]
cfctl r2 du <bucket>[/prefix] [--by ext|prefix|class] [--depth N] [--top N] [--json]
cfctl r2 object get|put|delete|stat ...        # wrangler-style aliases
```

- `ls` shows one level (folders + objects); `-r` lists everything under the prefix.
- `put` detects the content type from the extension, then by sniffing the
  bytes. A key ending in `/` gets the file's base name appended. Files up to
  `--multipart-threshold` (default 100 MB; the REST limit is 300 MB) go through
  the REST API. Larger files, and any upload that sets Cache-Control,
  Content-Disposition, Content-Encoding, Content-Language or `--meta`, go
  through R2's S3 API as a parallel multipart upload, with temporary
  credentials minted for that one key (failed uploads are aborted).
- `rm -r` lists the prefix, shows the count and size, asks, then deletes 1,000
  keys per request.
- Listing costs Class A operations; `du` and `ls -r` on big buckets add up
  (1 operation per 1,000 keys).

### Usage and cost

```bash
cfctl r2 usage [--scan] [--json]
```

One row per bucket: objects, size (Infrequent Access split out), Class A and
Class B operations this month and last month, plus an account row for
account-level operations (ListBuckets). Then an estimated bill for last month,
this month to date, and this month projected.

| | Standard | Infrequent Access |
|---|---|---|
| Storage | $0.015 / GB-month (10 GB free) | $0.01 / GB-month |
| Class A | $4.50 / million (1M free) | $9.00 / million |
| Class B | $0.36 / million (10M free) | $0.90 / million |
| Retrieval | free | $0.01 / GB |

Source: developers.cloudflare.com/r2/pricing, checked 2026-10-03 (one table
in `internal/r2/pricing.go`). Egress is free; DeleteObject, DeleteBucket and
AbortMultipartUpload are free. Class A is PutObject, CopyObject, multipart
operations, List*, PutBucket*, and lifecycle storage-tier transitions;
everything else is Class B. Billable units round up (one request over the free
tier bills a whole million). Storage is the daily maximum averaged over the
month. Sizes come from `r2StorageAdaptiveGroups`; `--scan` lists every object
instead. Estimates only; the invoice is authoritative.

### Bucket settings

```bash
cfctl r2 buckets cors list|set|delete <bucket> [--file rules.json]
cfctl r2 buckets lifecycle list <bucket>
cfctl r2 buckets lifecycle add <bucket> --id ID [--prefix P] [--expire-days N | --expire-date D]
     [--ia-transition-days N | --ia-transition-date D] [--abort-multipart-days N] [--disabled]
cfctl r2 buckets lifecycle remove <bucket> --id ID
cfctl r2 buckets lifecycle set <bucket> --file rules.json
cfctl r2 buckets lock list <bucket>
cfctl r2 buckets lock add <bucket> --id ID [--prefix P] (--retention-days N | --retention-date D | --retention-indefinite)
cfctl r2 buckets lock remove|set ...
cfctl r2 buckets domain list|get|add|update|remove <bucket> [--domain D] [--zone Z] [--min-tls 1.2] [--ciphers ...]
cfctl r2 buckets dev-url get|enable|disable <bucket>
cfctl r2 buckets local-uploads get|enable|disable <bucket>
cfctl r2 buckets notification list <bucket>
cfctl r2 buckets notification create <bucket> --queue Q --event-types object-create,object-delete [--prefix P] [--suffix S]
cfctl r2 buckets notification delete <bucket> --queue Q [--rule ID]...
cfctl r2 buckets sippy get|disable <bucket>
cfctl r2 buckets sippy enable <bucket> --provider aws|gcs|s3|azure [provider flags] --r2-access-key-id K --r2-secret-access-key S
cfctl r2 buckets jobs list|get <bucket> [job-id]
cfctl r2 buckets jobs create <bucket> --type prefix-delete --prefix tmp/ | --type storage-class-migration [--from C --to C]
cfctl r2 buckets catalog ...                   # R2 Data Catalog; same tree as `cfctl basin catalog`
```

Rules files take the API/wrangler shape `{"rules": [...]}` or a bare array.
`add`/`remove` read the current rules, change them, and write them back.

### Temporary credentials and sync

```bash
cfctl r2 temp-credentials <bucket> [--permission object-read-only|object-read-write|admin-read-only|admin-read-write]
     [--ttl 1h] [--prefix P]... [--object K]... [--parent-access-key-id ID] [--env] [--json]
cfctl r2 sync <dir> <bucket>[/prefix] [--dry-run] [--delete] [--exclude GLOB]... [--size-only] [--concurrency N] [--yes]
cfctl r2 sync <bucket>[/prefix] <dir> ...      # download direction
```

- Temporary credentials come from `POST /accounts/{id}/r2/temp-access-credentials`
  and can't exceed the parent R2 token; the parent defaults to the ID of
  cfctl's own API token (an API token with R2 permissions is also an S3 key
  pair). `--env` prints `AWS_*` exports for aws-cli, rclone, and so on.
- `sync` plans from a REST listing, so `--dry-run` works in read-only mode. A
  file is unchanged when the size matches and, unless `--size-only`, its MD5
  equals the object's ETag (multipart ETags fall back to size). Transfers use
  the S3 API with credentials scoped to the bucket and prefix. `--delete`
  removes destination-only files after asking.
- The S3 client uses cfctl's shared HTTP client, so `--read-only` and
  `--debug` apply to it too. Transfer commands raise the per-request timeout
  to 30 minutes unless `--timeout` is given. `CFCTL_R2_S3_ENDPOINT` overrides
  the endpoint.

## D1

```bash
cfctl d1 list [--name N] [--json]
cfctl d1 info <database>                       # alias get; adds 24h read/write query and row counts
cfctl d1 create <name> [--location weur] [--jurisdiction eu] [--read-replication auto|disabled]
cfctl d1 update <database> --read-replication auto|disabled
cfctl d1 delete <database> [--yes]

cfctl d1 execute <database> --command "SELECT ..." [--param V]... [--json]
cfctl d1 execute <database> --file schema.sql [--yes]   # >5 MB files use the import API
cfctl d1 export <database> --output backup.sql [--no-data | --no-schema] [--table T]...
cfctl d1 import <database> <file.sql> [--yes]

cfctl d1 time-travel info <database> [--timestamp T]
cfctl d1 time-travel restore <database> (--bookmark B | --timestamp T) [--yes]

cfctl d1 insights <database> [--since 1d|7d|24h] [--sort-by time|reads|writes|count] [--sort-direction asc|desc] [--limit N]

cfctl d1 migrations create <database> <message> [--migrations-dir migrations]
cfctl d1 migrations list <database> [--migrations-dir DIR] [--table d1_migrations]
cfctl d1 migrations apply <database> [--migrations-dir DIR] [--table T] [--yes]
```

- `execute` prints each statement's rows as a table (from the `/raw`
  endpoint, so column order is kept) followed by rows read/written, changes,
  and duration. `--json` prints the `/query` result: rows as objects plus meta.
  Running a file that writes asks first.
- `export` starts the export, polls until it finishes, and downloads the file
  from the pre-signed URL (no Cloudflare credentials are sent there).
  `import` uploads to the pre-signed URL, checks the ETag, and polls ingestion.
- Migrations are wrangler-compatible: `NNNN_message.sql` files applied in
  order, recorded in `d1_migrations` (`id`, `name`, `applied_at`). Each file
  is sent together with its `INSERT INTO d1_migrations` in one request.
  `apply` prints the current Time Travel bookmark first, so you can roll back
  with `cfctl d1 time-travel restore`. The directory comes from
  `--migrations-dir`; wrangler.toml isn't read. There is no local (Miniflare)
  mode.

## Queues

Queues, Worker and HTTP pull consumers, delivery control, messages, and event subscriptions. A queue can be given by **name or ID** everywhere. Generated equivalents: `cfctl api queue <op>`.

```bash
# Queues
cfctl queues list [--json]
cfctl queues get <queue>                       # alias: info
cfctl queues create <name> [--delivery-delay SECS] [--message-retention-period SECS] [--jurisdiction eu|us|fedramp] [--data JSON]
cfctl queues update <queue> [--delivery-delay SECS] [--message-retention-period SECS] [--name NEW] [--data JSON]
cfctl queues delete <queue> [--yes]
cfctl queues metrics <queue>

# Delivery
cfctl queues pause-delivery <queue>            # keeps the other settings
cfctl queues resume-delivery <queue>
cfctl queues purge <queue> [--yes]             # permanently deletes every message
cfctl queues purge status <queue>

# Consumers
cfctl queues consumer list <queue>
cfctl queues consumer get <queue> <consumer-id>
cfctl queues consumer add <queue> <script> [--batch-size N] [--batch-timeout SECS] [--message-retries N] \
    [--max-concurrency N] [--retry-delay SECS] [--dead-letter-queue Q]
cfctl queues consumer update <queue> <script-or-consumer-id> [same flags]   # unchanged settings are kept
cfctl queues consumer remove <queue> <script-or-consumer-id> [--yes]
cfctl queues consumer worker add|remove ...    # same as consumer add|remove
cfctl queues consumer http add <queue> [--batch-size N] [--message-retries N] [--retry-delay SECS] [--visibility-timeout MS]
cfctl queues consumer http remove <queue> [--yes]

# Messages
cfctl queues send <queue> <body|-|@file> [--content-type text|json] [--json-body] [--delay SECS]
cfctl queues send-batch <queue> <file.json|-> [--delay SECS]     # batches of 100
cfctl queues pull <queue> [--batch-size N] [--visibility-timeout MS]   # HTTP pull consumer; prints lease IDs
cfctl queues ack <queue> --ack LEASE[,LEASE] [--retry LEASE[,LEASE]] [--retry-delay SECS]
cfctl queues peek <queue> [--batch-size N]

# Event subscriptions (platform events delivered to a queue)
cfctl queues subscription list [queue]
cfctl queues subscription get <subscription-id>
cfctl queues subscription create <queue> --source <type> --events a,b [--name N] [--enabled=false] \
    [--model-name M] [--worker-name W] [--workflow-name F] [--script-tag T]
cfctl queues subscription update <subscription-id> [--name N] [--events a,b] [--enabled=false]
cfctl queues subscription delete <subscription-id> [--yes]
```

Sources: `images`, `kv`, `r2`, `superSlurper`, `vectorize`, `workersAi.model` (`--model-name`), `workersBuilds.worker` (`--worker-name`), `workers.script` (`--script-tag`), `workflows.workflow` (`--workflow-name`).

`send-batch` file elements are either message objects (`{"body": ..., "content_type": "json"|"text", "delay_seconds": N}`) or any JSON value, which is sent as a JSON message.

## Hyperdrive

Connection pooling and query caching for PostgreSQL and MySQL. A config can be given by **ID or name**. Passwords and Access client secrets are write-only: the API never returns them and cfctl never prints them. Generated equivalents: `cfctl api hyperdrive <op>`.

```bash
cfctl hyperdrive list
cfctl hyperdrive get <config>
cfctl hyperdrive create <name> --connection-string 'postgres://user:pass@host:5432/db'
cfctl hyperdrive create <name> --origin-host H --origin-port P --origin-scheme postgres|postgresql|mysql \
    --database D --origin-user U --origin-password PW
cfctl hyperdrive create <name> ... --access-client-id ID --access-client-secret S   # database behind a Tunnel
cfctl hyperdrive create <name> ... --service-id SVC                                 # database behind a Workers VPC
cfctl hyperdrive update <config> [any create flag] [--name NEW]
cfctl hyperdrive delete <config> [--yes]
cfctl hyperdrive restart <config>              # restart the connection pool
cfctl hyperdrive planetscale signature         # signed authorization for a Cloudflare-billed PlanetScale DB
```

Caching and TLS flags (create and update): `--caching-disabled`, `--max-age SECS`, `--swr SECS`, `--origin-connection-limit N`, `--sslmode MODE`, `--ca-certificate-id ID`, `--mtls-certificate-id ID`. Any field not covered by a flag: `--data JSON` (flags are merged on top).

## Vectorize

Vectorize v2 indexes, addressed by **name**. Generated equivalents: `cfctl api vectorize <op>`.

```bash
cfctl vectorize list
cfctl vectorize get <index>
cfctl vectorize info <index>                   # vector count, processed-up-to mutation
cfctl vectorize create <name> --dimensions 768 --metric cosine|euclidean|dot-product [--description D]
cfctl vectorize create <name> --preset @cf/baai/bge-base-en-v1.5
cfctl vectorize delete <index> [--yes]

cfctl vectorize insert <index> <file.ndjson|-> [--unparsable-behavior error|discard]   # batches of 5000 lines
cfctl vectorize upsert <index> <file.ndjson|-> [--unparsable-behavior error|discard]
cfctl vectorize query <index> --vector '[0.1,...]' | --vector-file q.json [--top-k N] \
    [--return-values] [--return-metadata none|indexed|all] [--filter '{"genre":"jazz"}']
cfctl vectorize get-vectors <index> <id>...
cfctl vectorize delete-vectors <index> <id>... [--yes]
cfctl vectorize list-vectors <index> [--count N] [--cursor C] [--all]

cfctl vectorize create-metadata-index <index> --property-name P --type string|number|boolean
cfctl vectorize list-metadata-index <index>
cfctl vectorize delete-metadata-index <index> --property-name P [--yes]
cfctl vectorize metadata-index create|list|delete ...   # same three, grouped
```

NDJSON lines look like `{"id": "1", "values": [0.1, 0.2], "metadata": {"genre": "jazz"}, "namespace": "a"}`. Writes are asynchronous; each batch returns a mutation ID.

## Secrets Store

Account-level stores and secrets. Secret **values are write-only and never printed**. A store or secret can be given by **name or ID**. Generated equivalents: `cfctl api secrets-store <op>`.

```bash
cfctl secrets-store quota
cfctl secrets-store store list
cfctl secrets-store store get <store>
cfctl secrets-store store create <name>
cfctl secrets-store store delete <store> [--force] [--yes]   # --force also deletes its secrets

cfctl secrets-store secret list <store> [--search S] [--scopes workers,...]
cfctl secrets-store secret get <store> <secret>
cfctl secrets-store secret create <store> <name> --scopes workers[,ai_gateway,...] [--comment C]
        # value: no-echo prompt on a terminal, stdin when piped, --value-file F, or --value V
cfctl secrets-store secret update <store> <secret> [--new-value | --value-file F | --value V] [--scopes ...] [--comment C]
cfctl secrets-store secret duplicate <store> <secret> <new-name> [--scopes ...] [--comment C]
cfctl secrets-store secret delete <store> <secret> [--yes]
```

Scopes: `workers`, `ai_gateway`, `dex`, `access`, `containers`, `websearch`.

```bash
printf %s "$STRIPE_KEY" | cfctl secrets-store secret create default_secrets_store STRIPE_KEY --scopes workers
```

## K2

K2 streams [open beta; requires Workers Paid]. A stream can be given by **ID or name**. Generated equivalents: `cfctl api workers-k2-other <op>`.

```bash
cfctl k2 streams list [--name SUBSTR]
cfctl k2 streams get <stream>
cfctl k2 streams create <name> [--retention SECS] [--http] [--http-auth] [--cors-origins O,...] [--worker-binding]
cfctl k2 streams update <stream> [--retention SECS] [--http=false] [--http-auth] [--cors-origins ...] [--worker-binding=false]
cfctl k2 streams delete <stream> [--yes]
cfctl k2 streams subscriptions <stream>
```

## Basin (Pipelines and Basin Catalog)

Cloudflare Pipelines (v1 API: streams → SQL pipelines → sinks; requires Workers Paid) and the Basin Catalog (an Apache Iceberg REST catalog on an R2 bucket). Pipelines, streams, and sinks can be given by **ID or name**. Generated equivalents: `cfctl api workers-pipelines-other <op>`, `cfctl api basin-catalog-management <op>`.

```bash
# Pipelines
cfctl basin pipelines list
cfctl basin pipelines get <pipeline>
cfctl basin pipelines create <name> --sql "INSERT INTO sink SELECT * FROM stream" | --sql-file F
cfctl basin pipelines validate-sql [SQL] [--sql-file F]
cfctl basin pipelines update <name> --legacy --data @pipeline.json   # legacy pipelines only
cfctl basin pipelines delete <pipeline> [--yes]

# Streams
cfctl basin pipelines streams list | get <stream> | delete <stream>
cfctl basin pipelines streams create <name> [--http] [--http-auth] [--cors-origins O] [--worker-binding] \
    [--format json|parquet] [--schema-file schema.json]
cfctl basin pipelines streams update <stream> [--http=false] [--http-auth] [--cors-origins O] [--worker-binding=false]

# Sinks
cfctl basin pipelines sinks list | get <sink> | delete <sink>
cfctl basin pipelines sinks create <name> --type r2 --bucket B [--path P] --access-key-id K --secret-access-key S \
    [--format json|parquet] [--compression C] [--file-prefix P] [--partitioning PATTERN] [--roll-size BYTES] [--roll-interval SECS]
cfctl basin pipelines sinks create <name> --type r2_data_catalog --bucket B --namespace N --table T --catalog-token TOKEN

# Catalog
cfctl basin catalog list
cfctl basin catalog get <bucket>
cfctl basin catalog enable <bucket>
cfctl basin catalog disable <bucket> [--yes]
cfctl basin catalog delete <bucket> [--force] [--yes]       # removes catalog metadata, keeps objects
cfctl basin catalog credential set <bucket> [--token T]     # or the token on stdin; never printed
cfctl basin catalog credential status <bucket>
cfctl basin catalog maintenance get <bucket>
cfctl basin catalog maintenance set <bucket> [--compaction enabled|disabled] [--target-size 64|128|256|512] \
    [--snapshot-expiration enabled|disabled] [--max-snapshot-age 14d] [--min-snapshots N]
cfctl basin catalog compaction enable|disable <bucket> [--target-size MB]
cfctl basin catalog snapshot-expiration enable|disable <bucket> [--max-age 7d] [--min-snapshots N]
cfctl basin catalog namespaces <bucket> [--parent NS] [--page-size N] [--page-token T]
cfctl basin catalog tables <bucket> <namespace> [--page-size N] [--page-token T]
cfctl basin catalog table get|maintenance|maintenance-runs <bucket> <namespace> <table>
cfctl basin catalog table set-maintenance <bucket> <namespace> <table> [maintenance flags]
cfctl basin catalog table run-maintenance <bucket> <namespace> <table> compaction|snapshot_expiration
```

The same catalog tree, against the R2 Data Catalog API (`/r2-catalog`), is available as `cfctl r2 bucket catalog ...`.

`basin sql query` (R2 SQL) is not part of the REST API; use wrangler for it. `basin pipelines setup` (wrangler's interactive wizard) has no equivalent: create the stream, sink, and pipeline with the three create commands.

## Artifacts

Hosted Git repositories [private beta]. Generated equivalents: `cfctl api artifacts <op>`.

```bash
cfctl artifacts namespaces list | get <ns> | create <ns> [--jurisdiction unrestricted|us|eu|fedramp] | delete <ns> [--yes]

cfctl artifacts repos list <ns> [--search S] [--sort created_at|updated_at|last_push_at|name]
cfctl artifacts repos get <ns> <repo>
cfctl artifacts repos create <ns> <repo> [--description D] [--default-branch B] [--read-only-repo]
cfctl artifacts repos fork <ns> <repo> <new-name> [--default-branch-only] [--read-only-repo]
cfctl artifacts repos import <ns> <repo> --url https://github.com/o/r.git [--branch B] [--depth N]
cfctl artifacts repos delete <ns> <repo> [--yes]
cfctl artifacts repos log <ns> <repo> [--ref R] [--limit N] [--offset N]
cfctl artifacts repos file <ns> <repo> <ref> <path> [--output F]   # raw bytes; --json for the API's JSON view
cfctl artifacts repos blob|commit|tree <ns> <repo> <hash>
cfctl artifacts repos tokens <ns> <repo> [--state active|expired|revoked|all]
cfctl artifacts repos issue-token <ns> <repo> [--scope read|write] [--ttl SECS]   # printed once
cfctl artifacts repos revoke-token <ns> <token-id> [--yes]
```

`--read-only-repo` marks the repository read-only; the global `--read-only` is cfctl's request guard.

## Agent Memory

Agent Memory namespaces, profiles, and memories [private beta]. Generated equivalents: `cfctl api namespaces <op>`, `cfctl api memory <op>`.

```bash
cfctl agent-memory namespace list | get <ns> | create <ns> | delete <ns> [--yes]
cfctl agent-memory profile list <ns>
cfctl agent-memory profile summary <ns> <profile> [--session S]
cfctl agent-memory profile delete <ns> <profile> [--yes]
cfctl agent-memory memories list <ns> <profile> [--session S] [--type fact|event|instruction|task]
cfctl agent-memory memories get <ns> <profile> <memory-id>
cfctl agent-memory memories remember <ns> <profile> <content|-> [--session S]
cfctl agent-memory memories recall <ns> <profile> "<question>" [--length short|medium|long] [--thinking low|medium|high] [--reference-date D]
cfctl agent-memory memories ingest <ns> <profile> --file chat.json [--session S]
cfctl agent-memory memories delete <ns> <profile> <memory-id> [--yes]
cfctl agent-memory session delete <ns> <profile> <session-id> [--yes]
```

`ingest` takes an array of `{"role": "user"|"assistant"|"system", "content": "...", "timestamp": "..."}` (or an object with a `messages` array).

## Wrangler parity

[`docs/wrangler-map/storage.tsv`](../wrangler-map/storage.tsv) maps every
wrangler storage command. Gaps are deliberate: D1 has no `--local` mode, and
`basin sql` (R2 SQL) and the interactive `pipelines setup` wizard aren't REST
API calls; use wrangler for those (see
[When to still use wrangler](../cfctl-vs-wrangler.md#when-to-still-use-wrangler)).
