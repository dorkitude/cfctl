# Platform & apps

[Docs index](../README.md) · [Core](core.md) · [Workers](workers.md) · [Storage](storage.md) · [Platform](platform.md) · [Zone & account](admin.md) · [API](../api.md) · [vs wrangler](../cfctl-vs-wrangler.md)

In the terminal: `cfctl docs platform`.

Hand-written commands for Pages, Workers AI, AI Search, Workflows, Containers,
Browser Run, Flagship, Email, Turnstile, Tunnels, Workers VPC, and mTLS
certificates, plus the meta commands `docs`, `completion`, and `init`.

Every command here also has a literal, generated equivalent under
`cfctl api <tag> <op>` (see `cfctl api tags` and [api.md](../api.md)).

> **Agents:** use `--json` on reads. It prints the API's `result` object
> unchanged (secrets still redacted unless `--reveal`).

## Conventions

- `--json` on every command prints the raw API result.
- Lists follow pagination automatically.
- Zones accept a name (`example.com`) or an ID. Tunnels and container apps
  accept a name or an ID. Workflow instance IDs accept `latest`.
- Destructive commands (`delete`, `rollback`, `disable`, `terminate`,
  `rotate-secret`, ...) ask for confirmation on a terminal and need `--yes`
  when stdin isn't one.
- Secrets returned by the API (tunnel tokens, Turnstile secrets, registry
  passwords) are redacted unless you pass `--reveal`.
- `--read-only` / `CFCTL_READONLY=1` refuses every write before it's sent,
  including `ai run`, `pages deploy`, and `containers images list` (which needs
  a POST for registry credentials).
- If a product isn't enabled for the account or the token lacks a permission,
  the error says so (for example: `HTTP 401: ... requires the Workers Paid plan;
  Containers isn't available to this account or token`).

## Pages

```bash
# Projects
cfctl pages project list
cfctl pages project get my-site                    # domains, build config, vars, bindings
cfctl pages project create my-site --production-branch main
cfctl pages project edit my-site --compatibility-date 2026-10-01 --compatibility-flags nodejs_compat
cfctl pages project delete my-site --yes
cfctl pages project purge-build-cache my-site

# Deploy a directory (Direct Upload, same flow as wrangler pages deploy)
cfctl pages deploy ./dist --project-name my-site
cfctl pages deploy ./dist --project-name my-site --branch preview-x
cfctl pages deploy ./dist --dry-run                # hash and list files, no API calls

# Deployments
cfctl pages deployment list my-site --env production
cfctl pages deployment get my-site <id>
cfctl pages deployment logs my-site <id>           # build logs
cfctl pages deployment tail my-site                # live Functions logs (latest production deployment)
cfctl pages deployment tail my-site <id> --status error --format json
cfctl pages deployment retry my-site <id>
cfctl pages deployment rollback my-site <id> --yes
cfctl pages deployment delete my-site <id> --force --yes

# Custom domains
cfctl pages domains list my-site
cfctl pages domains add my-site www.example.com
cfctl pages domains retry my-site www.example.com
cfctl pages domains delete my-site www.example.com --yes

# Secrets (values from a hidden prompt or stdin, never argv)
cfctl pages secret list my-site --env preview
printf %s "$API_KEY" | cfctl pages secret put my-site API_KEY
cfctl pages secret bulk my-site secrets.json       # {"NAME": "value", ...}
cfctl pages secret delete my-site API_KEY --yes

# Settings as a Wrangler config file
cfctl pages download config my-site -o wrangler.jsonc
```

`pages deploy` uploads every file in the directory except `node_modules`,
`.git`, `.DS_Store`, and `.wrangler`. It also sends `_headers`, `_redirects`, and
`_routes.json`, plus a pre-built `_worker.js` (one ES module, uploaded as-is).
It does not bundle a `functions/` directory: build it first with
`npx wrangler pages functions build --outdir dist/_worker.js`. Branch, commit
hash, message, and dirty flag default to the current git repo. Files are hashed
the way wrangler hashes them (blake3), so uploads are deduplicated across both
tools.

Not applicable (they need wrangler's local runtime or bundler):
`pages dev`, `pages functions build`.

## Workers AI

```bash
cfctl ai models list --task "Text Generation"
cfctl ai models search llama
cfctl ai models get @cf/meta/llama-3.1-8b-instruct
cfctl ai models schema @cf/meta/llama-3.1-8b-instruct
cfctl ai tasks
cfctl ai authors

# Inference (billed per use)
cfctl ai run @cf/meta/llama-3.1-8b-instruct --prompt "Write a haiku about DNS"
cfctl ai run @cf/meta/llama-3.1-8b-instruct --prompt "hi" --system "Be terse" --stream
cfctl ai run @cf/meta/llama-3.1-8b-instruct --data @input.json --json
cfctl ai run @cf/black-forest-labs/flux-1-schnell --prompt "a cloud" -o cloud.png
cfctl ai run @cf/openai/whisper --file talk.mp3 --json

# LoRA finetunes
cfctl ai finetune list
cfctl ai finetune public
cfctl ai finetune create @cf/mistral/mistral-7b-instruct-v0.2-lora my-ft ./adapter
cfctl ai finetune delete <id> --yes

# Documents to Markdown
cfctl ai markdown report.pdf page.html
```

## AI Search

Instances live in a namespace (`default` unless `-n/--namespace`).

```bash
cfctl ai-search list
cfctl ai-search get docs
cfctl ai-search create docs --type r2 --source my-bucket --chunk-size 512 --reranking
cfctl ai-search update docs --generation-model @cf/meta/llama-3.3-70b-instruct-fp8-fast
cfctl ai-search stats docs
cfctl ai-search search docs "how do I rotate keys?" --max-num-results 5 --filter lang=en
cfctl ai-search delete docs --yes

cfctl ai-search jobs list|create docs
cfctl ai-search jobs get|logs docs <job-id>
cfctl ai-search jobs cancel docs <job-id> --yes
cfctl ai-search items list docs
cfctl ai-search items get|chunks|logs docs <item-id>
cfctl ai-search namespace list|get|create|update|delete
cfctl ai-search tokens list
```

## Workflows

```bash
cfctl workflows list
cfctl workflows describe billing
cfctl workflows trigger billing '{"order": 42}' --id order-42
cfctl workflows delete billing --yes

cfctl workflows instances list billing --status errored
cfctl workflows instances describe billing latest     # steps, attempts, errors, output
cfctl workflows instances pause|resume billing <id>
cfctl workflows instances terminate billing <id> --rollback --yes
cfctl workflows instances restart billing <id> --from charge --yes
cfctl workflows instances send-event billing <id> approved --payload '{"ok":true}'
cfctl workflows instances delete billing <id> <id> --yes

cfctl workflows versions list billing
cfctl workflows versions graph billing <version-id>
```

## Containers

Requires the Workers Paid plan.

```bash
cfctl containers list
cfctl containers info web                  # name or ID
cfctl containers instances web
cfctl containers versions web
cfctl containers delete web --yes

# Managed registry (registry.cloudflare.com)
cfctl containers images list
cfctl containers images delete web:v1 --yes

# docker wrappers (docker must be installed; CFCTL_DOCKER overrides the binary)
cfctl containers build . -t web:v2 --push
cfctl containers push web:v2

# Other registries
cfctl containers registries list
cfctl containers registries configure <domain> --data '{"auth": {...}}'
cfctl containers registries credentials registry.cloudflare.com --permissions pull --reveal
cfctl containers registries delete <domain> --yes
```

`push` asks the API for 15-minute push credentials, runs
`docker login --password-stdin` (the password goes in on stdin, never in argv),
tags the image as `registry.cloudflare.com/<account>/<image>`, and pushes it.
`containers ssh` is not applicable (use wrangler).

## Browser Run

```bash
cfctl browser list
cfctl browser create --keep-alive 600       # prints the DevTools URL
cfctl browser view <session-id> --target example.com
cfctl browser close <session-id>

# One-shot rendering (billed browser time)
cfctl browser screenshot https://example.com -o shot.png
cfctl browser pdf https://example.com -o page.pdf
cfctl browser markdown https://example.com
cfctl browser content https://example.com --data '{"gotoOptions":{"waitUntil":"networkidle0"}}'
cfctl browser links https://example.com
```

## Flagship (feature flags)

```bash
cfctl flagship apps list|get|create|update|delete

cfctl flagship flags list <app-id>
cfctl flagship flags get <app-id> new-ui
cfctl flagship flags create <app-id> new-ui                       # boolean on/off, default off
cfctl flagship flags create <app-id> model -v stable=gpt-a -v candidate=gpt-b
cfctl flagship flags enable|disable <app-id> new-ui
cfctl flagship flags set <app-id> new-ui on                       # default variation
cfctl flagship flags rollout <app-id> new-ui --to on --percentage 25 --by user_id
cfctl flagship flags split <app-id> model -w stable=95 -w candidate=5 --by user_id
cfctl flagship flags rules <app-id> new-ui --rule-json '{"conditions":[...],"serve_variation":"on"}'
cfctl flagship flags evaluate <app-id> new-ui --context user_id=42 --context plan=pro
cfctl flagship flags changelog <app-id> new-ui
cfctl flagship flags delete <app-id> new-ui --yes
cfctl flagship flags pull <app-id> -o flagship-flags.json
```

`flags pull` writes every flag (key, description, enabled, default, variations,
rules) to a JSON file. wrangler imports flags into its local Miniflare store
instead.

## Email

```bash
# Email Routing
cfctl email routing list
cfctl email routing settings example.com
cfctl email routing enable example.com
cfctl email routing disable example.com --yes
cfctl email routing dns get example.com
cfctl email routing dns unlock example.com
cfctl email routing rules list example.com
cfctl email routing rules create example.com --to hi@example.com --forward me@gmail.com
cfctl email routing rules create example.com --to bot@example.com --worker inbox-worker
cfctl email routing rules update example.com <rule-id> --to hi@example.com --drop
cfctl email routing rules delete example.com <rule-id> --yes
cfctl email routing catch-all get example.com
cfctl email routing catch-all set example.com --forward me@gmail.com
cfctl email routing addresses list|get|create|delete

# Email Sending
cfctl email sending list [example.com]
cfctl email sending enable notifications.example.com
cfctl email sending settings notifications.example.com
cfctl email sending dns get notifications.example.com
cfctl email sending disable notifications.example.com --yes
cfctl email sending send --from me@notifications.example.com --to you@example.org \
  --subject "Hi" --text "Hello" --attachment report.pdf
cfctl email sending send-raw --from me@notifications.example.com --to you@example.org --mime-file msg.eml
```

Domains for Email Sending can be subdomains; cfctl finds the parent zone.

## Turnstile

```bash
cfctl turnstile widget list
cfctl turnstile widget get <sitekey> [--reveal]
cfctl turnstile widget create --name signup --domain example.com --mode managed
cfctl turnstile widget update <sitekey> --mode invisible           # other fields are kept
cfctl turnstile widget rotate-secret <sitekey> --yes [--reveal]
cfctl turnstile widget delete <sitekey> --yes
```

## Tunnels (cloudflared)

```bash
cfctl tunnel list
cfctl tunnel info home
cfctl tunnel create home                       # remotely managed
cfctl tunnel create home --local               # writes ~/.cloudflared/<id>.json (0600)
cfctl tunnel config get home
cfctl tunnel config set home --data '{"config":{"ingress":[{"hostname":"app.example.com","service":"http://localhost:8080"},{"service":"http_status:404"}]}}'
cfctl tunnel route dns home app.example.com    # proxied CNAME to <id>.cfargotunnel.com
cfctl tunnel route list
cfctl tunnel route add home 10.0.0.0/8 --comment office
cfctl tunnel route delete <route-id> --yes
cfctl tunnel vnet list
cfctl tunnel connections home
cfctl tunnel cleanup home --yes
cfctl tunnel token home --reveal               # the connector token is a secret
cfctl tunnel run home                          # cloudflared tunnel run, token via TUNNEL_TOKEN
cfctl tunnel quick-start localhost:8080        # free trycloudflare.com tunnel
cfctl tunnel delete home --yes
```

`run` and `quick-start` exec `cloudflared` (`CFCTL_CLOUDFLARED` overrides the binary).

## Workers VPC

```bash
cfctl vpc service list
cfctl vpc service get <service-id>
cfctl vpc service create --name db --type tcp --tcp-port 5432 --app-protocol postgresql \
  --ipv4 10.0.0.5 --tunnel-id <tunnel-uuid>
cfctl vpc service create --name api --type http --hostname api.internal \
  --resolver-ips 10.0.0.2 --tunnel-id <tunnel-uuid>
cfctl vpc service update <service-id> ...      # same flags; replaces the service
cfctl vpc service delete <service-id> --yes
```

## mTLS certificates

```bash
cfctl cert upload mtls-certificate --cert cert.pem --key key.pem --name client
cfctl cert upload certificate-authority --cert ca.pem --name my-ca
cfctl cert list
cfctl cert get <id>
cfctl cert associations <id>
cfctl cert delete <id> --yes

# wrangler mtls-certificate spelling
cfctl mtls-certificate upload --cert cert.pem --key key.pem
cfctl mtls-certificate list|get|delete
```

## Meta

```bash
cfctl docs                    # README, rendered (plain Markdown when piped)
cfctl docs --list             # topics: readme, changelog, api, core, workers, storage, ...
cfctl docs platform           # this page
cfctl docs --search tunnel

cfctl completion bash|zsh|fish|powershell

cfctl init my-worker                      # wrangler.jsonc, src/index.ts, package.json, tsconfig.json
cfctl init my-site --template assets      # static assets + API route
cfctl init jobs --template scheduled      # cron trigger
```

`cfctl dev`, `cfctl types`, and `cfctl setup` exist only to point you at
wrangler: they need the local workerd runtime or rewrite framework build config.

## Token permissions

Grant the API token what you use (account-level unless noted):

| Commands | Permission |
| --- | --- |
| pages | Cloudflare Pages: Read / Edit |
| ai | Workers AI: Read / Edit |
| ai-search | AI Search: Read / Edit (plus R2 for R2 sources) |
| workflows | Workers Scripts: Read / Edit |
| containers | Containers: Read / Edit (Workers Paid plan) |
| browser | Browser Rendering: Read / Edit |
| flagship | Flagship: Read / Edit |
| email routing | Email Routing Addresses (account), Zone: Email Routing Rules, Zone Settings |
| email sending | Email Sending: Read / Edit |
| turnstile | Turnstile Sites: Read / Edit |
| tunnel | Cloudflare Tunnel: Read / Edit (+ Zone DNS: Edit for `route dns`) |
| vpc | Connectivity Directory: Read / Edit |
| cert, mtls-certificate | Account SSL and Certificates: Read / Edit |

## Wrangler parity

[`docs/wrangler-map/platform.tsv`](../wrangler-map/platform.tsv) maps every
wrangler command in this area (auth, Pages, AI, Workflows, Containers, ...).
The not-applicable ones (`dev`, `pages dev`, `types`, `setup`,
`pages functions build`, `containers ssh`, OAuth profiles) are local-only; see
[When to still use wrangler](../cfctl-vs-wrangler.md#when-to-still-use-wrangler).
