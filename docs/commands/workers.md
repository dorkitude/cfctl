# Workers

[Docs index](../README.md) · [Core](core.md) · [Workers](workers.md) · [Storage](storage.md) · [Platform](platform.md) · [Zone & account](admin.md) · [API](../api.md) · [vs wrangler](../cfctl-vs-wrangler.md)

In the terminal: `cfctl docs workers`.

`cfctl workers ...` covers what `wrangler` does for Workers that the Cloudflare
API supports: uploading and deploying, versions and gradual rollouts,
rollbacks, secrets, triggers (crons, routes, custom domains, workers.dev),
live tail, stored logs, dispatch namespaces, and Previews.

Wrangler's top-level verbs have the same top-level shortcuts in cfctl:

```bash
cfctl deploy              # = cfctl workers deploy
cfctl tail                # = cfctl workers tail
cfctl rollback            # = cfctl workers rollback
cfctl secret ...          # = cfctl workers secret ...
cfctl versions ...        # = cfctl workers versions ...
cfctl deployments ...     # = cfctl workers deployments ...
cfctl triggers ...        # = cfctl workers triggers ...
cfctl dispatch-namespace ...
cfctl preview ...         # = cfctl workers preview ...
```

> **Agents:** pass `--json` on every read. Output is the API's own objects
> (secret values are never returned).

Every command goes through cfctl's shared request layer, so `--read-only` /
`CFCTL_READONLY=1` refuses anything that would change your account before it
is sent. The generated, literal equivalents
stay available under `cfctl api worker-script`, `cfctl api worker-versions`,
`cfctl api worker-deployments`, `cfctl api worker-routes`, `cfctl api domains`,
`cfctl api workers`, `cfctl api previews`, and so on.

## Which Worker?

Commands that act on one Worker take its name as an argument or `--name`. If
you give neither, cfctl reads `name` from the wrangler config in the current
directory (`wrangler.json`, `wrangler.jsonc`, or `wrangler.toml`, in
wrangler's order).

```bash
cfctl workers get my-worker              # argument
cfctl secret list --name my-worker       # --name
cd my-worker && cfctl deployments status # from ./wrangler.toml
cfctl deploy --config infra/wrangler.jsonc --env staging   # another file, an [env.staging] section
```

With `--env X`, cfctl applies the `[env.X]` section like wrangler: inheritable
keys (`main`, `compatibility_date`, `routes`, `triggers`, `assets`, ...) fall
back to the top level, bindings and `vars` don't, and the name becomes
`<name>-X` unless the section sets one.

## Browse Workers

```bash
# List every Worker in the account
cfctl workers list
cfctl workers ls --json

# One Worker: settings, bindings (secret values are never returned), workers.dev URL, crons, current deployment
cfctl workers get my-worker
cfctl workers get my-worker --json      # {"worker": ..., "settings": ..., "schedules": [...], "deployment": ...}
```

## Deploy

`deploy` uploads a Worker, deploys it to 100% of traffic, and applies its
triggers. cfctl doesn't bundle by default: point it at a built file (or a
plain multi-file ES module Worker, whose relative imports it uploads too), or
pass `--bundle` to run esbuild if it's on your PATH.

```bash
# Everything from ./wrangler.toml: name, main, compatibility date/flags, vars,
# bindings, assets, Durable Object migrations, crons, routes, custom domains
cfctl deploy

# A pre-built file, flags only
cfctl deploy dist/index.js --name my-worker --compatibility-date 2025-01-01 \
  --compatibility-flag nodejs_compat \
  --var API_BASE=https://api.example.com \
  --kv CACHE=0f2ac74b498b48028cb68387c421e279 \
  --r2 FILES=my-bucket \
  --d1 DB=6a4d2c0e-1111-2222-3333-444455556666 \
  --service AUTH=auth-worker#AuthEntrypoint \
  --queue JOBS=jobs \
  --ai AI \
  --binding '{"type":"browser","name":"BROWSER"}'

# Bindings from a JSON file (an array of API binding objects)
cfctl deploy dist/index.js --name my-worker --bindings-file bindings.json

# Bundle with esbuild first; minify; upload the source map
cfctl deploy src/index.ts --bundle --minify --upload-source-maps

# Static assets (with or without a Worker script)
cfctl deploy --name my-site --assets ./public
cfctl deploy --assets ./dist            # main + [assets] binding from the config

# Triggers from flags (added to the config's)
cfctl deploy --route "example.com/api/*" --domain api.example.com --cron "*/15 * * * *"
cfctl deploy --workers-dev=false        # turn workers.dev off
cfctl deploy --no-triggers              # upload only; leave triggers alone

# Annotate the version
cfctl deploy --message "fix login" --tag v1.4.2

# See exactly what would be sent (metadata, modules, triggers); sends nothing
cfctl deploy --dry-run
```

Flags shared by `deploy`, `versions upload`, and `preview deploy`:

```text
  --name string                 Worker name (default: from wrangler config)
  -c, --config string           wrangler.toml / wrangler.json(c) (default: found in the current directory)
  -e, --env string              Wrangler environment ([env.<name>])
  --compatibility-date string   YYYY-MM-DD (default: config, else today with a warning)
  --compatibility-flag strings  Repeatable
  --var / --kv / --r2 / --d1 / --service / --queue / --ai / --binding    Repeatable binding shorthands
  --bindings-file string        JSON array of binding objects
  --module strings              Extra module files to upload (repeatable)
  --esm / --service-worker      Force the script format (default: detected)
  --bundle, --minify            Bundle with esbuild (must be on PATH)
  --upload-source-maps          Upload <main>.map
  --assets string               Static assets directory
  --keep-vars                   Keep vars set in the dashboard
  --message, --tag string       Version annotations
  --dry-run                     Print the upload, send nothing
```

Notes:

- Existing secrets are always kept (`keep_bindings: secret_text, secret_key`).
  Plain-text and JSON vars are replaced by the config's unless `--keep-vars`
  (or `keep_vars = true`).
- Assets: `_headers` and `_redirects` are sent as assets config, and
  `.assetsignore` patterns are skipped. Only files Cloudflare doesn't already
  have are uploaded.
- Durable Object `[[migrations]]` are applied from the Worker's current
  migration tag onward.
- Cron triggers are replaced by the config's `[triggers] crons`. Routes and
  custom domains are added (or re-pointed at this Worker), never removed. The
  zone for a route is the account zone whose name matches its hostname
  (`--zone` or `zone_name` override). With no routes and no `workers_dev`
  setting, workers.dev is turned on, like wrangler.

## Versions and gradual rollouts

```bash
# Upload a version without sending it traffic (same inputs as deploy, triggers untouched)
cfctl versions upload --message "canary" --tag v1.5.0
cfctl versions upload dist/index.js --name my-worker --preview-alias staging

# List versions, newest first (default 10; 0 = all)
cfctl versions list my-worker
cfctl versions list my-worker --limit 0 --json

# One version: metadata, compatibility settings, bindings
cfctl versions view 8fa8b1b6 --name my-worker     # a unique prefix works
cfctl versions view latest --name my-worker

# Deploy one version to 100%
cfctl versions deploy latest --name my-worker

# Split traffic: 10% to the new version, the rest to the old one
cfctl versions deploy 8fa8b1b6@10 7005f4a4 --name my-worker --message "10% canary"
cfctl versions deploy 8fa8b1b6@50 7005f4a4@50 --name my-worker
```

### Secrets in a new version (not deployed)

```bash
# Each creates a new version from the latest one (other bindings inherited), without deploying it
cfctl versions secret put API_KEY --name my-worker
cfctl versions secret bulk secrets.json --name my-worker --message "rotate keys"
cfctl versions secret delete OLD_KEY --name my-worker --yes
cfctl versions secret list --name my-worker
# then roll it out
cfctl versions deploy latest@10 8fa8b1b6 --name my-worker
```

## Deployments and rollback

```bash
# Which versions serve traffic, newest deployment first (▶ = current)
cfctl deployments list my-worker
cfctl deployments status my-worker
cfctl deployments status my-worker --json

# Roll back to the previous deployment's versions (asks first)
cfctl rollback --name my-worker
cfctl rollback --name my-worker --yes --message "bad release"

# Roll back to a specific version
cfctl rollback 7005f4a4 --name my-worker --yes
```

## Secrets

Values come from a hidden prompt (on a terminal) or stdin. cfctl never prints
them, not in output, `--json`, errors, or `--debug` logs. Each change creates
and deploys a new version.

```bash
# Prompt (no echo)
cfctl secret put API_KEY --name my-worker

# From stdin (scripts, password managers)
printf %s "$API_KEY" | cfctl secret put API_KEY --name my-worker
pass show cloudflare/api-key | cfctl secret put API_KEY --name my-worker

# Names only (the API never returns values)
cfctl secret list --name my-worker

# Delete (asks first; --yes to skip)
cfctl secret delete API_KEY --name my-worker

# Many at once, in one version (max 100): JSON object (null deletes) or .env lines
cfctl secret bulk secrets.json --name my-worker        # {"A": "1", "OLD": null}
cfctl secret bulk .env.production --name my-worker     # KEY=value, export KEY="v", # comments
cat secrets.json | cfctl secret bulk --name my-worker
```

## Triggers

```bash
# Everything that sends traffic or events to a Worker
cfctl triggers list my-worker
cfctl triggers list my-worker --zone example.com      # only search one zone for routes

# Apply the config's triggers (after `versions upload`, like `wrangler triggers deploy`)
cfctl triggers deploy
cfctl triggers deploy my-worker --cron "0 * * * *" --route "example.com/api/*" --domain api.example.com
cfctl triggers deploy --dry-run
```

### Cron triggers

```bash
cfctl workers crons list my-worker
cfctl workers crons set my-worker --cron "*/15 * * * *" --cron "0 0 * * MON"   # replaces all
cfctl workers crons clear my-worker --yes
```

### Routes

```bash
# Every zone's Worker routes (or one zone, or one Worker)
cfctl workers routes list
cfctl workers routes list --zone example.com
cfctl workers routes list --name my-worker --json

# Route a pattern to a Worker (zone inferred from the hostname)
cfctl workers routes add "example.com/api/*" --name my-worker
cfctl workers routes add "*.example.com/*" --name my-worker --zone example.com

# Delete by pattern or route ID (asks first)
cfctl workers routes delete "example.com/api/*"
```

### Custom domains

```bash
cfctl workers domains list
cfctl workers domains list --name my-worker
cfctl workers domains add api.example.com --name my-worker
cfctl workers domains delete api.example.com --yes
```

### workers.dev

```bash
# The account's subdomain: <worker>.<subdomain>.workers.dev
cfctl workers subdomain get
cfctl workers subdomain set my-team --yes           # renames every workers.dev URL

# One Worker's workers.dev URL and preview URLs
cfctl workers dev-url get my-worker
cfctl workers dev-url enable my-worker --previews
cfctl workers dev-url disable my-worker
```

## Logs

### Live tail

`tail` creates a tail on the Worker, streams events over a WebSocket until
Ctrl-C (or until Cloudflare closes it), then deletes the tail. Filters run on
Cloudflare's side.

```bash
cfctl tail my-worker                          # pretty on a terminal
cfctl tail my-worker --format json | jq .     # one JSON object per line (default when piped)

# Filters
cfctl tail my-worker --status error           # ok | error | canceled (repeatable)
cfctl tail my-worker --method POST --method PUT
cfctl tail my-worker --header "x-debug:1"     # NAME or NAME:value
cfctl tail my-worker --ip 203.0.113.7
cfctl tail my-worker --search "checkout"      # console.log text
cfctl tail my-worker --sampling-rate 0.1
```

Pretty output looks like:

```text
GET https://example.com/api/cart - 200 Ok @ 2026-10-03 14:02:11
  (log) cart loaded 3
"*/5 * * * *" @ 2026-10-03 14:05:00 - Exception
  ✘ Error: upstream timed out
```

### Stored logs (Workers Logs)

For Workers with observability enabled. The query API is a POST, so it is
refused in read-only mode.

```bash
cfctl workers logs my-worker                          # last hour, newest first
cfctl workers logs my-worker --since 24h --search timeout --limit 200
cfctl workers logs my-worker --level error --json
```

## Dispatch namespaces (Workers for Platforms)

Needs the Workers for Platforms add-on (otherwise the API answers 403).

```bash
cfctl dispatch-namespace list
cfctl dispatch-namespace get customers
cfctl dispatch-namespace create customers
cfctl dispatch-namespace rename customers tenants
cfctl dispatch-namespace scripts tenants              # scripts in a namespace
cfctl dispatch-namespace delete tenants --yes         # deletes its scripts too
```

## Previews (open beta)

Worker Previews are named preview environments of a Worker (one per branch,
say), each with its own URL, deployments, and secrets. `--preview` defaults to
the current git branch.

```bash
# Upload + deploy to a Preview (created if it doesn't exist); ES modules only
cfctl workers preview deploy --preview feature-x
cfctl workers preview deploy dist/index.js --name my-worker --preview feature-x --var MODE=preview

# Browse
cfctl workers preview list my-worker
cfctl workers preview get --name my-worker --preview feature-x
cfctl workers preview deployments --name my-worker --preview feature-x

# Secrets on one Preview (each change creates a preview deployment)
cfctl workers preview secret put API_KEY --preview feature-x
cfctl workers preview secret list --preview feature-x
cfctl workers preview secret bulk secrets.json --preview feature-x
cfctl workers preview secret delete API_KEY --preview feature-x --yes

# Secrets in the base config that new Previews start from
cfctl workers preview base-config secret put API_KEY --name my-worker
cfctl workers preview base-config secret list --name my-worker

# Delete a Preview and its deployments
cfctl workers preview delete --preview feature-x --yes
```

## Confirmations and read-only mode

Destructive commands ask first on a terminal and need `--yes` (`-y`)
otherwise: `workers delete`, `rollback`, `secret delete`, deletions in
`secret bulk` / `versions secret bulk` / `preview secret bulk`,
`versions secret delete`, `routes delete`, `domains delete`, `crons clear`,
`subdomain set`, `dispatch-namespace delete`, `preview delete`,
`preview secret delete`.

With `--read-only` or `CFCTL_READONLY=1`, every read command works and every
write fails before anything is sent:

```bash
CFCTL_READONLY=1 cfctl workers get my-worker          # fine
CFCTL_READONLY=1 cfctl tail my-worker
# ✗ failed to start a tail on my-worker: refusing POST /client/v4/accounts/.../tails: read-only mode
```

## Wrangler parity

[`docs/wrangler-map/workers.tsv`](../wrangler-map/workers.tsv) lists every
wrangler command in this area with its cfctl equivalent and status (summary in
[cfctl vs wrangler](../cfctl-vs-wrangler.md)). Gaps: no built-in bundler (use `--bundle` with
esbuild, or a pre-built file); `deploy` and `triggers deploy` add routes and
custom domains but don't remove ones missing from the config; `tail` has no
`--ip self` and doesn't reconnect when a tail expires; local-only commands
(`wrangler dev`, `types`, ...) are out of scope here.
