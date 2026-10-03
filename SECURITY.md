# Security Policy

cfctl handles Cloudflare API tokens, so security reports are welcome and
taken seriously.

## Reporting a vulnerability

Please **don't open a public issue**. Use GitHub's private
[security advisory](https://github.com/dorkitude/cfctl/security/advisories/new)
form instead. You'll get a reply as soon as possible, and credit in the fix's
release notes if you'd like it.

## How cfctl treats your token

- It's stored only in `~/.config/cfctl/token` (file `0600`, folder `0700`), or
  read from `CFCTL_TOKEN` / `CLOUDFLARE_API_TOKEN`.
- It is never printed: not in output, errors, `--json`, or `--debug` logs
  (tests check this).
- It's only sent to `api.cloudflare.com` (and, for R2 transfers, to R2's S3
  endpoint using short-lived credentials minted for that one job).
- `--read-only` / `CFCTL_READONLY=1` refuses every write before it leaves your
  machine; use it when exploring or letting an agent drive.

## Supported versions

Only the latest release gets security fixes.
