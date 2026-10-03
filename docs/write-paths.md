# Write paths

Every hand-written cfctl command that sends POST, PUT, PATCH or DELETE, the
Cloudflare endpoint(s) it calls, and whether the request matches Cloudflare's
API. The generated `cfctl api <tag> <op>` commands are left out: they are built
straight from the OpenAPI spec.

Verdicts:

- **OK**: method, path, query, headers, content type and body match the spec,
  the docs, or wrangler.
- **FIXED**: something didn't match and was fixed on `feat/write-check`. The
  notes say what was wrong.
- **UNCERTAIN**: can't be settled without a real write. The notes say why.

Abbreviations: `{a}` = `/accounts/{account_id}`, `{z}` = `/zones/{zone_id}`.

## How verified

1. **Request shape.** For each command we read the request(s) it builds:
   method, path, query, headers, content type, JSON field names, types and
   nesting, and multipart part names and filenames.
2. **Spec.** Each request was compared with the embedded OpenAPI spec
   (`internal/apispec/openapi.json.gz`), through `cfctl api describe` or by
   decoding the spec directly. A script checked that every method and path in
   the table exists in the spec, except where the notes say otherwise.
3. **Docs and wrangler.** Where the spec is vague or silent, we used
   developers.cloudflare.com: the Workers static-assets direct upload, the
   Rulesets "update rule" page, and the R2 S3 compatibility pages. We also used
   for refrence wrangler's bundled source (`wrangler-dist/cli.js`) for:
   - Workers upload metadata, bindings and assets
   - Pages direct upload
   - D1 import and export
   - Queues settings
   - tail filters
   - Flagship
   - email
   - AI Search
5. **Tests.** Every fix has a regression test against the in-process fake API
   (`cmd/*_write_test.go`). Each test asserts the exact request that is sent,
   and fails on the old code.
6. **Safety.** No real write was made. The only live calls were read-only
   `GET`s with `CFCTL_READONLY=1`; one of them confirmed that the R2 REST object
   path accepts `%2F` in keys.

Earlier, a real KV namespace create, put, get and delete cycle worked against
a live account.

## Workers

| command | method + endpoint(s) | verdict | notes |
|---|---|---|---|
| workers deploy | PUT {a}/workers/scripts/{name} (multipart: `metadata` + module parts) | FIXED | upload format, keep_bindings OK; config `preview_urls` was ignored and some binding shapes were wrong (see bindings rows) |
| deploy (DO migrations) | GET scripts, then metadata.migrations {old_tag,new_tag,steps} | OK | |
| deploy / versions upload / preview (assets) | POST {a}/workers/scripts/{name}/assets-upload-session {manifest}; POST {a}/workers/assets/upload?base64=true (multipart, part name + filename = hash, Bearer JWT) | FIXED | unknown types now `application/null` like wrangler; single-asset upload mode added |
| asset manifest hash | sha256(base64(content)+ext)[:32] | OK | wrangler uses BLAKE3, but the docs only require "a 32 hexadecimal character hash" |
| asset single-upload mode | POST {a}/workers/assets/upload/{hash} (raw, Bearer JWT) | UNCERTAIN | not in spec; copied from wrangler; only used when the session JWT asks for it |
| workers versions upload | POST {a}/workers/scripts/{name}/versions (multipart) | FIXED | sent logpush/observability (not accepted there); allowed service-worker scripts though `main_module` is required |
| workers versions deploy | POST …/scripts/{name}/deployments[?force=true] {strategy:percentage, versions[], annotations} | OK | |
| workers rollback | same, force=true | OK | |
| workers secret put | PUT …/scripts/{name}/secrets {name,text,type:secret_text} | OK | |
| workers secret delete | DELETE …/scripts/{name}/secrets/{secret} | OK | |
| workers secret bulk | PATCH …/scripts/{name}/secrets-bulk | OK | same as wrangler |
| workers versions secret put/delete/bulk | PATCH {a}/workers/workers/{name}/versions/latest {bindings (inherit + secret_text), annotations} | OK | |
| workers triggers deploy / crons set / clear | PUT …/scripts/{name}/schedules [{cron}] | OK | |
| workers routes add | GET/POST {z}/workers/routes {pattern,script}; PUT {z}/workers/routes/{id} | OK | |
| workers routes delete | DELETE {z}/workers/routes/{id} | OK | |
| workers domains add | PUT {a}/workers/domains {hostname,service,environment,zone_id,zone_name} | OK | |
| workers domains delete | DELETE {a}/workers/domains/{id} | OK | |
| workers subdomain set | PUT {a}/workers/subdomain {subdomain} | OK | |
| workers dev-url enable/disable | POST …/scripts/{name}/subdomain {enabled, previews_enabled?} | OK | |
| deploy workers.dev step | same | FIXED | config `preview_urls` now sent as `previews_enabled` |
| workers delete | DELETE …/scripts/{name}[?force=true] | OK | |
| dispatch-namespace create | POST {a}/workers/dispatch/namespaces {name} | OK | |
| dispatch-namespace rename | PATCH …/dispatch/namespaces/{ns} {name} | OK | wrangler uses PUT; the spec has both |
| dispatch-namespace delete | DELETE …/dispatch/namespaces/{ns} | OK | |
| workers preview deploy | GET/POST {a}/workers/workers/{w}/previews; POST …/previews/{p}/deployments (JSON modules) | OK | the spec allows JSON; wrangler sends multipart |
| workers preview delete | DELETE …/previews/{p} | OK | |
| workers preview secret put/delete/bulk | PATCH …/previews/{p}/deployments/latest {env} | OK | |
| preview base-config secret | PATCH {a}/workers/workers/{w} {previews_base_config:{env}} | OK | |
| tail create / delete | POST …/scripts/{name}/tails {filters}; DELETE …/tails/{id} | OK | filter shapes match wrangler |
| workers logs (query) | POST {a}/workers/observability/telemetry/query | OK | read-style POST |
| workflows trigger | POST {a}/workflows/{wf}/instances {instance_id, params} | OK | |
| workflows instances pause/resume/terminate | PATCH …/instances/{id}/status {status} | OK | |
| workflows instances restart | same | FIXED | `from` was a string; API takes `{name,count?,type?}` |
| workflows instances send-event | POST …/instances/{id}/events/{type} | OK | |
| workflows instances delete | POST …/instances/batch/delete {instances} | OK | |
| workflows delete | DELETE {a}/workflows/{wf} | OK | |
| containers delete | DELETE {a}/containers/applications/{id} | OK | |
| containers registries configure | POST {a}/containers/registries | FIXED | required `kind` was missing; `is_public:true` is rejected by the API |
| containers registries delete | DELETE {a}/containers/registries/{domain} | OK | |
| containers registries credentials | POST …/registries/{domain}/credentials | OK | |
| containers images delete | registry DELETE /v2/{acct}/{img}/manifests/{tag}; PUT /v2/gc/layers | FIXED | deleted by digest, which removed every tag on that manifest; now deletes by tag like wrangler |
| binding: d1 | {type:d1, id} | UNCERTAIN | spec says `database_id`; wrangler's upload sends `id` (kept) |
| binding: kv_namespace / r2_bucket / durable_object_namespace | as spec | OK | |
| binding: service | {service, entrypoint?, environment?, props?} | FIXED | `props` was dropped |
| binding: queue | {queue_name, delivery_delay?} | FIXED | `delivery_delay` was dropped |
| binding: dispatch_namespace | {namespace, outbound:{worker,params}} | FIXED | `outbound` was passed through in config shape |
| binding: pipelines | {pipeline} | FIXED | config `stream` key wasn't accepted |
| bindings: hyperdrive, vectorize, analytics_engine, mtls_certificate, send_email, workflow, secrets_store_secret, ai, browser, images, media, version_metadata, assets, plain_text, json | as spec | OK | |

## KV and R2

| command | method + endpoint(s) | verdict | notes |
|---|---|---|---|
| kv namespace create | POST {a}/storage/kv/namespaces {title, jurisdiction?} | OK | |
| kv namespace rename | PUT …/namespaces/{id} {title} | OK | |
| kv namespace delete | DELETE …/namespaces/{id} | OK | |
| kv key put | PUT …/namespaces/{id}/values/{key} ?expiration_ttl\|expiration; raw, or multipart value+metadata | FIXED | key escaping left `:` `@` `+` `&` `=` `$` raw; now matches encodeURIComponent (wrangler) |
| kv key delete | DELETE …/values/{key} | FIXED | same key escaping |
| kv bulk put | PUT …/namespaces/{id}/bulk, JSON array, batches of 10,000 | OK | |
| kv bulk delete | POST …/bulk/delete, JSON array of strings | OK | current (not deprecated) endpoint |
| kv bulk get | POST …/bulk/get {keys,type?,withMetadata} | OK | read-only POST |
| r2 buckets create | POST {a}/r2/buckets {name, locationHint?, storageClass?} + cf-r2-jurisdiction | OK | |
| r2 buckets update | PATCH {a}/r2/buckets/{b}, header cf-r2-storage-class | OK | |
| r2 buckets delete [--force] | DELETE {a}/r2/buckets/{b} (+ batched DELETE …/{b}/objects) | OK | |
| r2 buckets cors set / delete | PUT / DELETE …/{b}/cors | OK | |
| r2 buckets lifecycle add/remove/set | GET, then PUT …/{b}/lifecycle | OK | |
| r2 buckets lock add/remove/set | GET, then PUT …/{b}/lock | OK | |
| r2 buckets domain add / update / remove | POST …/{b}/domains/custom; PUT / DELETE …/custom/{domain} | OK | |
| r2 buckets dev-url enable/disable | PUT …/{b}/domains/managed {enabled} | OK | |
| r2 buckets local-uploads enable/disable | PUT …/{b}/local-uploads {enabled} | OK | |
| r2 buckets notification create / delete | PUT / DELETE {a}/event_notifications/r2/{b}/configuration/queues/{queue_id} | OK | |
| r2 buckets sippy enable / disable | PUT / DELETE …/{b}/sippy | OK | |
| r2 buckets jobs create | POST …/{b}/jobs | OK | |
| r2 buckets catalog * | see Basin / R2 Data Catalog rows | OK | shared code |
| r2 put (REST) | PUT …/{b}/objects/{key} (+ cf-r2-storage-class) | FIXED | S3-style storage-class names are now mapped to REST names |
| r2 put (S3: multipart or with metadata) | S3 PutObject / CreateMultipartUpload / UploadPart / Complete / Abort | FIXED | sent `x-amz-storage-class: InfrequentAccess`; R2 S3 takes STANDARD / STANDARD_IA |
| r2 rm / object delete | DELETE …/objects/{key}; batched DELETE …/{b}/objects | OK | |
| r2 temp-credentials | POST {a}/r2/temp-access-credentials | OK | |
| r2 sync (upload / --delete) | S3 PutObject / multipart; S3 DeleteObjects | UNCERTAIN | aws-sdk-go-v2 sends a CRC32 checksum, not Content-MD5, on DeleteObjects; R2 documents CRC32 support but this wasn't tried live |

## D1, Queues, Hyperdrive, Vectorize, Secrets Store, K2, Basin, Artifacts, Agent Memory

| command | method + endpoint(s) | verdict | notes |
|---|---|---|---|
| d1 create | POST {a}/d1/database | OK | |
| d1 update | PATCH {a}/d1/database/{id} | OK | |
| d1 delete | DELETE {a}/d1/database/{id} | OK | |
| d1 execute (≤5 MB) | POST …/{id}/raw or /query {sql, params} | OK | |
| d1 import / execute --file >5 MB | POST …/import init, then PUT upload_url, then POST ingest, then POST poll | FIXED | an init answer with an upload_url could be re-sent in a loop; now uploads whenever upload_url is present (wrangler) |
| d1 export | POST …/export (polling), then GET signed_url | OK | |
| d1 time-travel restore | POST …/time_travel/restore?bookmark\|timestamp | OK | |
| d1 migrations apply | POST …/query | OK | same pattern as wrangler |
| queues create | POST {a}/queues | OK | |
| queues update | PATCH {a}/queues/{id} | FIXED | now sends queue_name and merges current settings (wrangler) |
| queues pause-delivery / resume-delivery | GET + PATCH {a}/queues/{id} | FIXED | now sends queue_name |
| queues delete | DELETE {a}/queues/{id} | OK | |
| queues purge | POST …/{id}/purge {delete_messages_permanently:true} | OK | |
| queues consumer add (worker / http) | POST …/{id}/consumers | OK | |
| queues consumer update | PUT …/consumers/{cid} (merged) | OK | |
| queues consumer remove | DELETE …/consumers/{cid} | OK | |
| queues send / send-batch | POST …/{id}/messages, /messages/batch | OK | |
| queues pull / peek / ack | POST …/messages/pull, /peek, /ack | OK | |
| queues subscription create / update / delete | POST / PATCH / DELETE {a}/event_subscriptions/subscriptions[/{id}] | OK | |
| hyperdrive create / update / delete | POST / PATCH / DELETE {a}/hyperdrive/configs[/{id}] | OK | |
| hyperdrive restart | POST …/configs/{id}/restart | OK | |
| hyperdrive planetscale signature | POST …/integrationsOperations/planetScale/createDatabaseSignature | OK | |
| vectorize create / delete | POST / DELETE {a}/vectorize/v2/indexes[/{name}] | OK | |
| vectorize insert / upsert | POST …/{name}/insert\|upsert (application/x-ndjson) | UNCERTAIN | matches spec and API docs; wrangler sends multipart part `vectors` instead |
| vectorize query / get-vectors / delete-vectors | POST …/query, /get_by_ids, /delete_by_ids | OK | |
| vectorize metadata-index create / delete | POST …/metadata_index/create, /delete | OK | |
| secrets-store create / delete | POST / DELETE {a}/secrets_store/stores[/{id}] | OK | |
| secrets-store secret create | POST …/stores/{sid}/secrets [array] | OK | |
| secrets-store secret update / duplicate / delete | PATCH, POST …/duplicate, DELETE | OK | |
| k2 streams create | POST {a}/k2/streams | OK | |
| k2 streams update | PATCH {a}/k2/streams/{id} | FIXED | explicit `--http=false` was flipped to true by `--http-auth` / `--cors-origins` |
| k2 streams delete | DELETE {a}/k2/streams/{id} | OK | |
| basin pipelines create / validate-sql / delete | POST {a}/pipelines/v1/pipelines, /validate_sql; DELETE | OK | |
| basin pipelines update --legacy | PUT {a}/pipelines/{name} | OK | deprecated endpoint |
| basin streams create / delete | POST / DELETE {a}/pipelines/v1/streams[/{id}] | OK | |
| basin streams update | PATCH {a}/pipelines/v1/streams/{id} | FIXED | required http.enabled / http.authentication now come from the current stream (`--cors-origins` alone turned auth off) |
| basin sinks create / delete | POST / DELETE {a}/pipelines/v1/sinks | OK | |
| catalog enable / disable / delete | POST …/{bucket}/enable, /disable, /delete[?force] | OK | basin + r2 catalog |
| catalog credential set | POST …/{bucket}/credential {token} | OK | |
| catalog maintenance set / table set-maintenance / run-maintenance | POST …/maintenance-configs, …/{type}/queue | OK | target_size_mb sent as a string, per spec |
| artifacts namespace create / delete | POST / DELETE {a}/artifacts/namespaces[/{ns}] | OK | |
| artifacts repos create / fork / import / delete | POST …/repos, …/fork, …/import; DELETE | OK | |
| artifacts repos issue-token / revoke-token | POST / DELETE …/{ns}/tokens | OK | |
| agent-memory namespace create / delete | POST / DELETE {a}/agent-memory/namespaces | OK | |
| agent-memory profile delete / summary | DELETE …/profiles/{p}; POST …/summary | OK | |
| agent-memory memories remember / recall / ingest / delete | POST …/remember, /recall, /ingest; DELETE …/memories/{id} | OK | |
| agent-memory session delete | DELETE …/sessions/{sid} | OK | |

## Pages, AI, Browser, Flagship, Email, Turnstile

| command | method + endpoint(s) | verdict | notes |
|---|---|---|---|
| pages project create / edit / delete | POST / PATCH / DELETE {a}/pages/projects[/{p}] | OK | |
| pages project purge-build-cache | POST …/{p}/purge_build_cache | OK | |
| pages deploy: upload-token | GET …/{p}/upload-token | OK | |
| pages deploy: check-missing / upload / upsert-hashes | POST /pages/assets/check-missing, /upload, /upsert-hashes (JWT) | OK | base64 payload, 40 MiB / 2000-file buckets; blake3 hash as in wrangler |
| pages deploy: create deployment | POST …/{p}/deployments (multipart) | FIXED | `_worker.js/` directories were dropped; file limit hard-coded at 20,000 (now from the JWT); commit_message truncation could split UTF-8 |
| pages deployment delete / retry / rollback | DELETE …?force=true; POST …/retry, /rollback | OK | |
| pages deployment tail | POST / DELETE …/deployments/{id}/tails | OK | |
| pages domains add / retry / delete | POST …/domains; PATCH / DELETE …/domains/{d} | OK | |
| pages secret put / bulk / delete | PATCH …/projects/{p} deployment_configs.env.env_vars | OK | delete sends null |
| ai run | POST {a}/ai/run/{model} | OK | |
| ai finetune create / delete | POST {a}/ai/finetunes + …/{id}/finetune-assets (multipart); DELETE | OK | |
| ai markdown | POST {a}/ai/tomarkdown (multipart `files`) | OK | |
| ai-search create / update | POST …/namespaces/{ns}/instances; PUT …/instances/{id} | FIXED | `--type builtin` sent `type:"builtin"`, outside the API enum |
| ai-search delete | DELETE …/instances/{id} | OK | |
| ai-search search | POST …/instances/{id}/search | OK | |
| ai-search jobs create | POST …/instances/{id}/jobs | FIXED | sent no body; now JSON `{}` / `{description}` |
| ai-search jobs cancel | PATCH …/jobs/{job} {action:cancel} | OK | |
| ai-search items delete | DELETE …/items/{item} | OK | |
| ai-search namespace create / update / delete | POST / PUT / DELETE …/namespaces | OK | |
| browser create / close | POST {a}/browser-rendering/devtools/browser; DELETE …/{session} | OK | |
| browser screenshot / pdf / markdown / content / links | POST {a}/browser-rendering/{action} | OK | |
| flagship apps create / update / delete | POST / PUT / DELETE {a}/flagship/apps | OK | |
| flagship flags create / update / delete | POST / PUT / DELETE …/apps/{app}/flags | OK | |
| flagship flags enable / disable / set / rollout / split / rules | GET, then PUT …/flags/{key} | OK | same fields as wrangler |
| email routing enable / disable / dns unlock | POST {z}/email/routing/enable, /disable, /unlock | OK | |
| email routing rules create / update / delete / catch-all | POST / PUT / DELETE {z}/email/routing/rules; PUT …/catch_all | OK | |
| email routing addresses create / delete | POST / DELETE {a}/email/routing/addresses | OK | |
| email sending enable / disable | POST / DELETE {z}/email/sending/subdomains | OK | |
| email sending send / send-raw | POST {a}/email/sending/send, /send_raw | OK | |
| turnstile widget create / update / delete / rotate-secret | POST / PUT / DELETE {a}/challenges/widgets; POST …/rotate_secret | OK | |

## Zones, settings, SSL, rules, tunnels

| command | method + endpoint(s) | verdict | notes |
|---|---|---|---|
| tunnel create | POST {a}/cfd_tunnel | OK | |
| tunnel delete | DELETE {a}/cfd_tunnel/{id} | OK | |
| tunnel cleanup | DELETE {a}/cfd_tunnel/{id}/connections | OK | |
| tunnel config set | PUT {a}/cfd_tunnel/{id}/configurations {config} | OK | |
| tunnel route add / delete | POST {a}/teamnet/routes; DELETE …/routes/{id} | OK | |
| tunnel route dns | POST {z}/dns_records, or PUT …/{id} with --overwrite | OK | |
| vpc service create / update | POST {a}/connectivity/directory/services; PUT …/{id} | FIXED | `--data` alone was rejected by the flag checks |
| vpc service delete | DELETE …/services/{id} | OK | |
| cert (mtls) upload / delete | POST / DELETE {a}/mtls_certificates | OK | |
| zones create | POST /zones | UNCERTAIN | `jump_start` is no longer in the spec |
| zones delete / pause / unpause / activation-check | DELETE {z}; PATCH {z} {paused}; PUT {z}/activation_check | OK | |
| zones settings set | PATCH {z}/settings/{setting} {value} | FIXED | min_tls_version / origin_max_http_version were sent as numbers |
| cache purge | POST {z}/purge_cache | OK | |
| cache dev-mode | PATCH {z}/settings/development_mode | OK | |
| ssl mode | PATCH {z}/settings/ssl | OK | |
| ssl universal | PATCH {z}/ssl/universal/settings | UNCERTAIN | the spec body has only `enabled`; `certificate_authority` (`--ca`) is from Cloudflare's docs |
| ssl packs order / delete | POST {z}/ssl/certificate_packs/order; DELETE …/{id} | OK | |
| ssl origin create / revoke | POST /certificates; DELETE /certificates/{id} | OK | an account-owned token may be refused (user tokens work) |
| ssl custom upload / delete | POST / DELETE {z}/custom_certificates | OK | |
| rulesets rules add | POST …/rulesets/{id}/rules, or entrypoint PUT / POST | OK | |
| rulesets rules update | GET ruleset, then PATCH …/rulesets/{id}/rules/{rule_id} | FIXED | PATCH replaces the whole rule (per the docs), so flags not passed were reset; now merges onto the current rule |
| rulesets rules delete / rulesets delete | DELETE …/rules/{rule_id}; DELETE …/rulesets/{id} | OK | |
| redirects add / delete / bulk add | entrypoint PUT / POST; DELETE rule; POST {a}/rules/lists/{id}/items | OK | |
| transform add / delete | entrypoint PUT / POST; DELETE rule | OK | |
| waf custom add / delete | entrypoint PUT / POST; DELETE rule | OK | |
| page-rules delete | DELETE {z}/pagerules/{id} | OK | |

## Account, DNS and the rest

| command | method + endpoint(s) | verdict | notes |
|---|---|---|---|
| firewall access-rules create | POST {z}\|{a}/firewall/access_rules/rules | FIXED | ASN was a bare number; spec value is a string like `AS12345` |
| firewall access-rules delete | DELETE …/rules/{id} | OK | |
| lists create / delete | POST / DELETE {a}/rules/lists | OK | |
| lists items-add / items-remove | POST / DELETE {a}/rules/lists/{id}/items | OK | |
| lb create / update / delete | POST {z}/load_balancers; PATCH / DELETE …/{id} | OK | |
| lb pools create / update / delete | POST {a}/load_balancers/pools; PATCH / DELETE | OK | |
| lb monitors create / delete | POST / DELETE {a}/load_balancers/monitors | OK | |
| members remove | DELETE {a}/members/{id} | OK | |
| tokens create / delete | POST / DELETE {a}/tokens (or /user/tokens) | OK | |
| logpush jobs create / update / delete | POST / PUT / DELETE {z}\|{a}/logpush/jobs | OK | |
| notifications policies create / delete | POST / DELETE {a}/alerting/v3/policies | OK | |
| dns dnssec enable / disable | PATCH {z}/dnssec {status} | OK | |
| dns settings set | PATCH {z}/dns_settings | OK | |
| dns import | POST {z}/dns_records/import (multipart) | FIXED | the `file` part had no filename; now has filename + text/plain |
| records create / update / delete | POST {z}/dns_records; PATCH / DELETE …/{id} | OK | |
| domains autorenew on/off | PATCH {a}/registrar/registrations/{domain} {auto_renew} | OK | |
| healthchecks create / delete | POST / DELETE {z}/healthchecks | OK | |
| waiting-rooms create / delete | POST / DELETE {z}/waiting_rooms | OK | |
| spectrum apps create / delete | POST / DELETE {z}/spectrum/apps | OK | |
| api request METHOD PATH | user-supplied | OK | raw passthrough |
