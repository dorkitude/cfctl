# AGENTS.md

Guidance for coding agents (Claude Code, Codex, ...) working on cfctl.
`CLAUDE.md` is a symlink to this file.

## Project Overview

cfctl is a CLI for Cloudflare Registrar domains, zones, and DNS, built in Go with
Cobra + Viper and lipgloss for styled output. It is a sibling of
[simple](https://github.com/dorkitude/simple) (the DNSimple CLI): keep command
names, flags, output, and file layout parallel to simple wherever Cloudflare's
model allows.

It uses a Cloudflare **API token** against REST API v4 via the official SDK
`github.com/cloudflare/cloudflare-go/v7` (not wrangler, not GraphQL).

## Build & Test

```bash
go build -o cfctl .     # Build
go vet ./...
go test ./...           # In-process CLI tests against a fake API (no network)
```

## Project Structure

- `main.go` - Entry point (version injected by GoReleaser via ldflags)
- `cmd/` - Cobra commands (root, auth, whoami, domains, autorenew, zones, records)
- `cmd/*_test.go` - Tests; `fake_test.go` holds the fake Cloudflare API + `runCLI`
- `internal/client/` - SDK client construction, account + zone resolution, error formatting
- `internal/config/` - Viper config + token storage in `~/.config/cfctl/`
- `internal/ui/` - Lipgloss palette and styled helpers
- `internal/output/` - JSON output helper

## Key Dependencies

- [cobra](https://github.com/spf13/cobra) - CLI framework
- [viper](https://github.com/spf13/viper) - Config + env binding
- [lipgloss](https://github.com/charmbracelet/lipgloss) - Terminal styling
- [cloudflare-go/v7](https://github.com/cloudflare/cloudflare-go) - Official Cloudflare SDK

## Config

- Token: `~/.config/cfctl/token` (0600, dir 0700). `CFCTL_TOKEN` /
  `CLOUDFLARE_API_TOKEN` override it.
- Account ID: `~/.config/cfctl/config.yaml` (`account_id`). `--account` /
  `CFCTL_ACCOUNT_ID` override it.
- `CFCTL_CONFIG_DIR` moves the whole directory.
- `CFCTL_API_BASE_URL` points the client at another API base (used by tests).

## Rules

- **Never print the token**: not in output, errors, `--json`, or logs. Format
  SDK errors with `client.APIError` / `apiErr`, which only include the status
  and API messages. Tests assert the token never appears (`assertNoToken`).
- Never write the token into `config.yaml`. `config.Save` uses a fresh Viper
  instance because the shared one also holds env-bound values.
- Support `--json` on every command that returns data.
- Registrar uses the GA Registrations API
  (`/accounts/{id}/registrar/registrations`); the older `/registrar/domains`
  endpoints are deprecated.
- New commands get a test against the fake API in `cmd/fake_test.go`.
- Don't run anything against a real Cloudflare account in tests.

## Releasing

Tag and push: `git tag vX.Y.Z && git push origin vX.Y.Z`. The `release`
workflow runs vet + tests, then GoReleaser publishes the binaries.
