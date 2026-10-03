# cfctl

A terminal client for Cloudflare Registrar domains, zones, and DNS.

`cfctl` is a sibling of [`simple`](https://github.com/dorkitude/simple) (the
DNSimple CLI): same command layout, same auth flow, same output style. It talks
to the Cloudflare REST API v4 with an **API token**, because `wrangler` has no
Registrar or DNS commands and its OAuth login can't get Registrar/DNS write
scopes.

- Cobra CLI (`cfctl auth`, `cfctl domains`, `cfctl zones`, `cfctl records`, ...)
- Viper config (`~/.config/cfctl/config.yaml`) with env-var overrides
- Official SDK: [`cloudflare-go/v7`](https://github.com/cloudflare/cloudflare-go)
- JSON output (`--json`) for scripting

## Highlights

- Token login with hidden prompt **or** piped stdin, validated before saving
- Account discovery (pick one when the token sees several)
- Registrar domains list/get with expiry and auto-renew (`↻`)
- Auto-renew status and on/off toggle (`cfctl domains autorenew`)
- Zones list/get/export, records list/get/create/update/delete
- Zones accepted by name (`example.com`) or ID; record names relative or FQDN
- The token is never printed: not in output, errors, or `--json`

## Requirements

- Go `1.26+`
- A Cloudflare API token (see `cfctl auth setup`)

## Installation

The repo is private, so tell Go not to use the public proxy for it:

```bash
go env -w GOPRIVATE='github.com/dorkitude/*'
gh auth setup-git            # lets git (and so go) fetch private GitHub repos
go install github.com/dorkitude/cfctl@latest
```

### Build locally

```bash
git clone https://github.com/dorkitude/cfctl.git
cd cfctl
go build -o cfctl .
```

### Release binaries

Pushing a `vX.Y.Z` tag runs GoReleaser (`.github/workflows/release.yml`) and
attaches darwin/linux amd64/arm64 tarballs to the GitHub release.

## Quick Start

```bash
cfctl auth setup      # how to create the token
cfctl auth login      # paste it (hidden), or pipe it in
cfctl auth status
cfctl whoami
cfctl domains list
```

### Logging in without the token touching anything else

`auth login` reads the token from a hidden prompt when stdin is a terminal, and
from stdin when it is piped. Either way it never echoes the token.

```bash
# On the machine itself (or over `ssh -t`, which gives you a terminal):
cfctl auth login
ssh -t myhost cfctl auth login

# Piped (e.g. from a password manager or clipboard on your laptop):
printf '%s' "$CLOUDFLARE_API_TOKEN" | cfctl auth login
pbpaste | ssh myhost cfctl auth login
```

If the token can see several accounts, `login` asks you to pick one (when a
terminal is available) or tells you to pass `--account <id>`.

## CLI Guide

### Global flags

- `--json` output JSON instead of styled text
- `--account <id>` override the cached Cloudflare account ID
- `--no-color` disable colored output (also honors `NO_COLOR`)
- `--version` print the version

### Commands

#### Authentication

```bash
cfctl auth login                 # hidden prompt, or piped stdin
cfctl auth login --account <id>  # choose an account explicitly
cfctl auth login --force         # replace an existing saved token
cfctl auth logout
cfctl auth status
cfctl auth setup
```

#### Whoami

```bash
cfctl whoami
cfctl whoami --json
```

Shows the token (ID, status, expiry), the account, and the user if the token
also has User Details: Read (not required).

#### Domains (Cloudflare Registrar)

```bash
cfctl domains list
cfctl domains list --filter example
cfctl domains get example.com

# Auto-renew
cfctl domains autorenew example.com          # show status
cfctl domains autorenew example.com on       # enable
cfctl domains autorenew example.com off      # disable
cfctl domains autorenew example.com --json
```

`autorenew` accepts `on`/`off` (also `enable`/`disable`). It is a no-op if the
domain is already in the requested state (`"changed": false` in `--json`), and
gives a clear error if the domain is not registered with Cloudflare Registrar
in this account (for example, a zone whose domain lives at another registrar).

#### Zones

```bash
cfctl zones list
cfctl zones list --filter example
cfctl zones get example.com
cfctl zones file example.com     # BIND export
```

#### Records

```bash
cfctl records list example.com
cfctl records list example.com --type A
cfctl records list example.com --name www
cfctl records get example.com <record-id>

cfctl records create example.com --type A --name www --content 1.2.3.4
cfctl records create example.com --type A --name www --content 1.2.3.4 --proxied
cfctl records create example.com --type MX --name @ --content mail.example.com --priority 10
cfctl records update example.com <record-id> --content 5.6.7.8
cfctl records update example.com <record-id> --proxied=false --ttl 300
cfctl records delete example.com <record-id>
```

Cloudflare specifics:

- Record IDs are 32-character hex strings (not integers as at DNSimple).
- `--ttl 1` (the default for `create`) means **automatic**; lists show `auto`.
- `--proxied` turns on the orange cloud; lists mark proxied records `☁ proxied`.
- Names may be `@`/empty (apex), relative (`www`), or fully qualified.
- `update` only sends the flags you pass (PATCH).

### JSON output for automation

```bash
cfctl domains list --json | jq '.[] | {domain_name, expires_at, auto_renew}'
cfctl records list example.com --json | jq '.[] | {id, type, name, content}'
```

## Configuration and Credential Storage

By default, everything lives in:

```text
~/.config/cfctl/        (mode 0700)
├── token               (mode 0600, the API token)
└── config.yaml         (mode 0600, account_id; managed with Viper)
```

Environment overrides (highest priority first):

| Setting | Flag | Env | File |
|---|---|---|---|
| Token | — | `CFCTL_TOKEN`, then `CLOUDFLARE_API_TOKEN` | `token` |
| Account ID | `--account` | `CFCTL_ACCOUNT_ID` | `config.yaml` |
| Config dir | — | `CFCTL_CONFIG_DIR` | — |

The token is never written to `config.yaml`, even when it comes from an env var.

## Token permissions

Create a **Custom Token** at My Profile → API Tokens with:

| Scope | Permission | Access |
|---|---|---|
| Account | Account Settings | Read |
| Account | Domain Registration (Registrar: Domains) | Edit |
| Zone | Zone | Edit |
| Zone | DNS | Edit |

Account resources: your account. Zone resources: all zones.

## Development

```bash
go build -o cfctl .
go vet ./...
go test ./...
gofmt -w $(find . -name '*.go')
```

Tests run the real Cobra commands in-process against a fake Cloudflare API
(`httptest`), so they need no token or network. See `AGENTS.md`.

## License

See `LICENSE`.
