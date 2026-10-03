# Core: auth, whoami, domains, zones, records

[Docs index](../README.md) · [Core](core.md) · [Workers](workers.md) · [Storage](storage.md) · [Platform](platform.md) · [Zone & account](admin.md) · [API](../api.md) · [vs wrangler](../cfctl-vs-wrangler.md)

In the terminal: `cfctl docs core`.

The commands cfctl started with: logging in, checking who you are, Cloudflare
Registrar domains, zones, and DNS records. They mirror
[`simple`](https://github.com/dorkitude/simple) (the DNSimple CLI): same verbs,
flags, and output style.

> **Agents:** pass `--json` on every read. Output is the API's own objects,
> never styled text, and the token never appears in it.

## Conventions

- **Zones** are accepted by name (`example.com`) or ID everywhere.
- **Record names** may be `@` or empty (apex), relative (`www`), or fully
  qualified (`www.example.com`).
- **Lists** fetch every page unless `--page` is given (`--per-page` sets the
  page size).
- **Destructive commands** ask first on a terminal; `--yes` / `-y` skips the
  prompt, and without a terminal `--yes` is required.
- `--read-only` / `CFCTL_READONLY=1` refuses every write before it's sent.

## Authentication

```bash
cfctl auth setup                 # how to create the token, and which permissions it needs
cfctl auth login                 # asks for the account ID, then the token (hidden)
cfctl auth login --account <id>  # skip the account ID prompt (required when piping)
cfctl auth login --force         # replace an existing saved token
cfctl auth status                # where the token comes from, its type, the account
cfctl auth status --verify       # ...and check it against the API
cfctl auth logout                # remove the saved token
```

### Logging in

`auth login` asks for your **account ID first** (unless `--account` is given
or one is cached in `config.yaml`): 32 hex characters, shown on the dashboard
account home ("Account ID"), in the dashboard URL, or by `wrangler whoami`.
Then it reads the token from a hidden prompt when stdin is a terminal, or from
stdin when piped. A piped token needs `--account <id>` (there is no terminal
to ask on). It never echoes the token.

```bash
# On the machine itself (or over `ssh -t`, which gives you a terminal):
cfctl auth login
ssh -t myhost cfctl auth login

# Piped (from a password manager or the clipboard on your laptop):
printf '%s' "$CLOUDFLARE_API_TOKEN" | cfctl auth login --account <account-id>
pbpaste | ssh myhost cfctl auth login --account <account-id>
```

The token is verified before it's saved, against that account ID directly (no
account discovery), with timeouts, so login can't hang.

### User tokens and account tokens

Both kinds work:

| Kind | Where to create it | Prefix | Verified via |
|---|---|---|---|
| **User token** | My Profile → API Tokens | (none) | `/user/tokens/verify` |
| **Account-owned token** | Manage Account → Account API Tokens | `cfat_` | `/accounts/{id}/tokens/verify` |

The type is recorded in `config.yaml` (`token_type`) and shown by `whoami` and
`auth status`. A few endpoints only work with a user token: `cfctl user`
(your profile, invites, memberships) and legacy Page Rules on some accounts.
cfctl says so when you hit one.

### Token permissions

For the core commands, create a **Custom Token** with:

| Scope | Permission | Access |
|---|---|---|
| Account | Account Settings | Read |
| Account | Domain Registration (Registrar: Domains) | Edit |
| Zone | Zone | Edit |
| Zone | DNS | Edit |

Account resources: your account. Zone resources: all zones. Other command
groups need their product's permission (Workers Scripts, Workers KV Storage,
R2, D1, Pages, ...); a 403 error names the likely missing one. `cfctl tokens
permission-groups` lists them all.

## Whoami

```bash
cfctl whoami
cfctl whoami --json
```

Shows the token (ID, status, expiry, type), the account, and the user if the
token also has User Details: Read (not required).

## Domains (Cloudflare Registrar)

```bash
cfctl domains list
cfctl domains list --filter example
cfctl domains list --json | jq '.[] | {domain_name, expires_at, auto_renew}'
cfctl domains get example.com

# Auto-renew
cfctl domains autorenew example.com          # show status
cfctl domains autorenew example.com on       # enable
cfctl domains autorenew example.com off      # disable
cfctl domains autorenew example.com --json
```

- Lists show the expiry date and mark auto-renewing domains with `↻`.
- `autorenew` accepts `on`/`off` (also `enable`/`disable`). It is a no-op if
  the domain is already in the requested state (`"changed": false` in
  `--json`), and gives a clear error if the domain isn't registered with
  Cloudflare Registrar in this account (for example, a zone whose domain lives
  at another registrar).
- cfctl uses the GA Registrations API
  (`/accounts/{id}/registrar/registrations`).

## Zones

```bash
cfctl zones list
cfctl zones list --filter example
cfctl zones get example.com
cfctl zones file example.com          # BIND zone file export
```

Zone lifecycle and settings (`zones create|delete|pause|unpause|activation-check`,
`zones settings list|get|set`) are documented with the other admin commands in
[admin.md](admin.md#zone-settings-and-lifecycle). DNSSEC, DNS settings, and
zone file import live under `cfctl dns` ([admin.md](admin.md#dns-extras)).

## Records

```bash
cfctl records list example.com
cfctl records list example.com --type A
cfctl records list example.com --name www
cfctl records get example.com <record-id>

cfctl records create example.com --type A --name www --content 192.0.2.1
cfctl records create example.com --type A --name www --content 192.0.2.1 --proxied
cfctl records create example.com --type CNAME --name blog --content example.com
cfctl records create example.com --type MX --name @ --content mail.example.com --priority 10
cfctl records create example.com --type TXT --name @ --content "v=spf1 include:_spf.google.com ~all"
cfctl records update example.com <record-id> --content 192.0.2.2
cfctl records update example.com <record-id> --proxied=false --ttl 300
cfctl records delete example.com <record-id>          # asks first; --yes to skip
```

Cloudflare specifics:

- Record IDs are 32-character hex strings (not integers as at DNSimple).
- `--ttl 1` (the default for `create`) means **automatic**; lists show `auto`.
- `--proxied` turns on the orange cloud; lists mark proxied records `☁ proxied`.
- `update` only sends the flags you pass (PATCH).
- `--comment` sets a record comment on `create` and `update`.
- For record types that need structured `data` (SRV, CAA, HTTPS, ...), batch
  changes, or filters beyond `--type`/`--name`, use the generated commands:
  `cfctl api describe dns-records-for-a-zone create-dns-record`
  (see [api.md](../api.md)).

## Configuration and credential storage

```text
~/.config/cfctl/        (mode 0700)
├── token               (mode 0600, the API token)
└── config.yaml         (mode 0600, account_id and token_type; managed with Viper)
```

| Setting | Flag | Env | File |
|---|---|---|---|
| Token | — | `CFCTL_TOKEN`, then `CLOUDFLARE_API_TOKEN` | `token` |
| Account ID | `--account` | `CFCTL_ACCOUNT_ID` | `config.yaml` |
| Config dir | — | `CFCTL_CONFIG_DIR` | — |
| Zone for `api`/`graphql`/`--zone` commands | `--zone` | `CFCTL_ZONE` | — |
| Read-only guard | `--read-only` | `CFCTL_READONLY=1` | — |
| Debug log | `--debug` | `CFCTL_DEBUG=1` | — |
| Colors | `--no-color` | `NO_COLOR` | — |

Flags win over env vars, which win over the files. The token is never written
to `config.yaml`, even when it comes from an env var. To keep several
accounts, point `CFCTL_CONFIG_DIR` at one directory per account (or set
`CFCTL_TOKEN` and `CFCTL_ACCOUNT_ID` per project).
