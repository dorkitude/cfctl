# CONTRIBUTING

Thanks for contributing to `cfctl`.

## Scope

`cfctl` is a Cloudflare Registrar + DNS client with a Cobra CLI for scripting
and direct operations. It mirrors [simple](https://github.com/dorkitude/simple)
(the DNSimple CLI); keep the two consistent.

## Prerequisites

- Go `1.26+`
- A Cloudflare API token for real-account testing (optional; tests use a fake API)

## Local Setup

```bash
git clone https://github.com/dorkitude/cfctl.git
cd cfctl
go build -o cfctl .
```

## Development Workflow

```bash
gofmt -w $(find . -name '*.go')
go vet ./...
go test ./...
```

### Smoke checks

```bash
./cfctl --help
./cfctl auth status
./cfctl auth setup
```

## Project Structure (High-Level)

- `cmd/` - Cobra commands (CLI entrypoints) and their tests
- `internal/client/` - Cloudflare client construction, account/zone resolution, errors
- `internal/config/` - Viper config and token persistence
- `internal/output/` - JSON output helper
- `internal/ui/` - shared CLI styling
- `main.go` - binary entrypoint

## CLI Contribution Guidelines

- Support `--json` where a command returns structured data
- Never print the token, including in errors; use `apiErr` for SDK errors
- Add a test against the fake API (`cmd/fake_test.go`) for every new command
- Prefer additive subcommands/flags over breaking changes

## Commit Style

Conventional commit prefixes are preferred: `feat:`, `fix:`, `docs:`,
`refactor:`, `chore:`.

## Release Notes / Roadmap

- `README.md` for current behavior and installation
- `ROADMAP.md` for planned features
