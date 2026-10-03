# AGENTS.md

Guidance for coding agents (Claude Code, Codex, ...) working on cfctl.
`CLAUDE.md` is a symlink to this file.

## Project Overview

cfctl is a CLI for Cloudflare, built in Go with Cobra + Viper and lipgloss for
styled output: Registrar domains, zones, DNS, every API-backed wrangler command
(Workers, KV, R2, D1, Queues, Pages, AI, ...), zone and account admin, and a
generated command for every Cloudflare API operation. It is a sibling of
[simple](https://github.com/dorkitude/simple) (the DNSimple CLI): keep command
names, flags, output, and file layout parallel to simple wherever Cloudflare's
model allows.

It uses a Cloudflare **API token** against REST API v4, via the official SDK
`github.com/cloudflare/cloudflare-go/v7` for hand-written commands and a raw
client (`internal/api`) for `cfctl api` / `cfctl graphql`. `cfctl api` has a
generated command for every operation in Cloudflare's OpenAPI spec.

**Read `docs/ARCHITECTURE.md` first**: package layout, the request layer, the
read-only guard, generated commands, and how to add a command group.

## Build & Test

```bash
make build              # go build -o cfctl .
make vet
make test               # In-process CLI tests against a fake API (no network)
go test ./cmd -run TestKV   # prefer targeted runs; `make race` is slow on small VMs
make smoke              # read-only smoke test against a real account (needs a token)
```

## Project Structure

- `main.go` - Entry point (an ldflags version overrides `internal/version`)
- `cmd/` - Cobra commands, one file (or a few) per group; `root.go` groups the
  top-level commands by area (`rootGroups`); `api_gen.go` builds the generated
  `cfctl api <tag> <op>` tree
- `cmd/*_test.go` - Tests; `fake_test.go` holds the fake Cloudflare API + `runCLI`
- `internal/api/` - Request layer: read-only guard, retries, timeouts, debug
  transport; raw client, envelope, pagination, multipart
- `internal/apispec/` - Embedded OpenAPI spec + generated ops table (`ops_gen.go`,
  don't edit; `go generate ./...`) + `api describe` rendering
- `internal/version/` - `Version = "0.2.NNN"`
- `internal/client/` - SDK client construction, account + zone resolution, error formatting
- `internal/config/` - Viper config + token storage in `~/.config/cfctl/`
- `internal/ui/` - Lipgloss palette and styled helpers
- `internal/output/` - JSON output helper
- `docs/` - reference docs (`docs/README.md` is the index), embedded by
  `docs_embed.go` for `cfctl docs`; `docs/wrangler-map/*.tsv` maps every
  wrangler command

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
- Don't run anything against a real Cloudflare account in tests. For manual
  checks against a real account, export `CFCTL_READONLY=1` first.
- Every HTTP request must go through `api.NewHTTPClient()` (the SDK gets it
  via `option.WithHTTPClient`). That is where the read-only guard lives; never
  build another `http.Client`.
- Destructive commands confirm, with `--yes` to skip; no prompt in read-only mode.
- Bump `internal/version` (`0.2.NNN`) and add a `CHANGELOG.md` entry at each
  meaningful milestone.
- Keep docs in step with code: a new or renamed command or flag updates its
  `docs/commands/*.md` page (and the README if it's in the Quick Start); a
  wrangler-equivalent command updates `docs/wrangler-map/*.tsv` and
  `docs/cfctl-vs-wrangler.md`. Every documented example must exist
  (`make docs-check`); regenerate the wrangler tables with `make wrangler-doc`.

## Releasing

Only the maintainer releases. Version `0.2.NNN` is tagged `v0.2.N`
(`0.2.012` → `git tag v0.2.12`); pushing the tag runs the `release` workflow
(vet + tests, then GoReleaser). Agents must not create or push tags.
