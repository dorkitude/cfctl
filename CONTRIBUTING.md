# Contributing to cfctl

Thanks for contributing! This repo aims to keep changes simple, focused, and
tested. cfctl's design follows [linctl](https://github.com/dorkitude/linctl)
(the Linear CLI): keep verbs, flags, output style, and docs consistent with it
where Cloudflare's model allows.

## Development

- Requirements: Go `1.26+`, `git`, `gh` (to fetch the private repo), `jq`
  (for examples), `golangci-lint` (optional), `esbuild` and `docker`
  (optional, only for `deploy --bundle` and `containers build`).
- A Cloudflare API token is **not** needed for tests; they run against a fake
  API. You only need one for `make smoke` and manual checks.

```bash
git clone https://github.com/dorkitude/cfctl.git
cd cfctl
make build          # ./cfctl
./cfctl --help
```

Useful targets (`make help` lists them):

- `make build`: build `./cfctl`
- `make install`: `go install` into `$(go env GOPATH)/bin`
- `make test`: tests against the fake Cloudflare API (no token, no network)
- `make race`: the same with `-race` (slow on small machines; run it before
  a release, not on every change)
- `make vet` / `make fmt` / `make fmt-check`
- `make lint`: `fmt-check` + `vet`, plus `golangci-lint` if installed
- `make generate`: regenerate `internal/apispec/ops_gen.go` (after changing
  `internal/apispec/gen`)
- `make spec`: refresh the vendored Cloudflare OpenAPI spec and regenerate
- `make docs-check`: build, then check every `cfctl ...` example in the
  README and `docs/` against `--help` (`scripts/docs-check`; fails on unknown
  subcommands or flags)
- `make wrangler-doc`: regenerate the summary and per-command tables in
  `docs/cfctl-vs-wrangler.md` from `docs/wrangler-map/*.tsv`
  (`scripts/gen-wrangler-doc.py`; `--check` reports drift without writing)
- `make smoke`: read-only smoke test of the built binary against your real
  account (`scripts/smoke.sh`, run with `CFCTL_READONLY=1`)

While iterating, prefer targeted tests: `go test ./cmd -run TestR2`.

## Project structure

- `cmd/`: Cobra commands and their tests; `api_gen.go` builds the generated
  `cfctl api <tag> <op>` tree; `fake_test.go` holds the fake Cloudflare API
- `internal/api/`: the request layer (read-only guard, retries, timeouts,
  debug log, raw client, pagination, multipart)
- `internal/apispec/`: the embedded OpenAPI spec and generated operations table
- `internal/client/`: SDK client, account and zone resolution, error formatting
- `internal/config/`: Viper config and token storage
- `internal/ui/`, `internal/output/`: styling and JSON output
- `internal/version/`: the version string
- `docs/`: reference docs, embedded into the binary by `docs_embed.go`

Read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) before changing anything
non-trivial.

## Adding a command

Follow [Adding a hand-written command group](docs/ARCHITECTURE.md#adding-a-hand-written-command-group)
in ARCHITECTURE.md. In short:

1. Build requests through the shared session (`newAPISession` / the SDK
   client from `internal/client`), never a new `http.Client`, so the
   read-only guard, retries, and `--debug` apply.
2. Support `--json` on everything that returns data (print the API's own
   objects).
3. Destructive commands confirm, with `--yes` / `-y` to skip.
4. Accept names *and* IDs for resources where the API allows lookup.
5. Add the command to its area group in `cmd/root.go` (`rootGroups`) if it's
   top-level.
6. Test it against the fake API (see below).
7. Document it in the matching `docs/commands/*.md` page (and the README
   Quick Start if it's an everyday command). If it covers a wrangler command,
   update `docs/wrangler-map/*.tsv` (columns `wrangler`, `cfctl`, `status`,
   `notes`; status `full`, `partial`, or `not-applicable`), run
   `make wrangler-doc`, and adjust the hand-written quick matrix if needed.
8. Run `make docs-check`.

Keep the generated `cfctl api` commands as they are: hand-written groups are
the ergonomic layer on top, not a replacement.

## Testing rules

- **Fake server only.** Tests run the real Cobra commands in-process against
  an `httptest` fake Cloudflare API (`cmd/fake_test.go`). Never call the real
  API from a test.
- **Read-only against real accounts.** For manual checks and `make smoke`,
  export `CFCTL_READONLY=1` first. The guard refuses every write before it is
  sent, so a typo can't change anything.
- **Never print tokens.** Not in output, errors, `--json`, `--debug` logs, or
  test failure messages. Format SDK errors with `apiErr` / `client.APIError`.
  Tests assert the token never appears (`assertNoToken`); keep doing that for
  new commands.
- `TestCoverage` checks every spec operation has exactly one generated
  command; run `make test` after `make spec`.
- `make docs-check` after renaming or removing a command or flag: every
  documented example must still exist. Deliberate stubs (`dev`, `types`, ...)
  and placeholders are allowlisted at the top of `scripts/docs-check`.

## Versioning

- `internal/version/version.go` holds the version as `0.MINOR.NNN`
  (`0.2.001`, `0.2.002`, ...). Bump `NNN` at each meaningful milestone.
- `0.2.NNN` isn't valid semver (leading zeros), so it is never a git tag. A
  release of `0.2.NNN` is tagged **`v0.2.N`**: drop the leading zeros
  (`0.2.007` → `v0.2.7`, `0.2.012` → `v0.2.12`).

## Changelog

- Every version bump gets a `## [0.2.NNN] - YYYY-MM-DD` section in
  `CHANGELOG.md` ([Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
  headings: Added / Changed / Fixed / Docs).
- In-progress notes go under `## [Unreleased]`.
- Keep entries user-facing: command names, flags, and behavior, not internal
  refactors.

## Commit style

Conventional prefixes are preferred: `feat:`, `fix:`, `docs:`, `refactor:`,
`chore:`, `test:`.

## Release Checklist

Only the maintainer releases. Agents must not create or push tags.

1) Prepare
- Make sure the README, `docs/`, and `--help` text match behavior.
- Bump `internal/version` if needed and finish the `CHANGELOG.md` section for
  this version (move entries out of `Unreleased`, add the date).
- Run `make lint`, `make race`, `make docs-check`, and `make smoke` (read-only, real account).
- Draft release notes from `CHANGELOG.md` (highlights, fixes, breaking
  changes).

2) Merge
- Merge the release branch into `main` and pull it.

3) Tag and release (`0.2.NNN` → `v0.2.N`)
- Create and push the tag:
  ```bash
  git tag v0.2.N -a -m "v0.2.N (0.2.NNN): short summary"
  git push origin v0.2.N
  ```
- The tag push runs `.github/workflows/release.yml` (vet + tests, then
  GoReleaser), which attaches darwin/linux amd64/arm64 tarballs to the GitHub
  release. Paste the release notes into it:
  ```bash
  gh release edit v0.2.N --notes "<highlights/fixes>"
  ```

4) Validate
  ```bash
  GOPRIVATE='github.com/dorkitude/*' go install github.com/dorkitude/cfctl@v0.2.N
  cfctl --version
  cfctl docs | head -n 5
  CFCTL_READONLY=1 cfctl whoami
  ```

5) Housekeeping
- Add a fresh `## [Unreleased]` section to `CHANGELOG.md` if the release
  consumed it.
- Update `ROADMAP.md`.
