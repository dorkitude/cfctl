// Package version holds cfctl's version string.
//
// cfctl counts versions as 0.MINOR.NNN (e.g. 0.2.001, 0.2.002, ...). That is
// not valid semver, so release tags use v0.MINOR.N instead (0.2.007 → v0.2.7).
// See CHANGELOG.md and docs/ARCHITECTURE.md.
package version

// Version is the version printed by `cfctl --version`. Release builds may
// override it with -ldflags "-X github.com/dorkitude/cfctl/internal/version.Version=...".
var Version = "0.2.007"
