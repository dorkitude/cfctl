// Package version holds cfctl's version string.
//
// cfctl uses semver with large patch numbers so small changes have room
// (0.2.710, 0.2.711, ...). The release tag is the version with a "v" prefix.
// See CHANGELOG.md and CONTRIBUTING.md.
package version

// Version is the version printed by `cfctl --version`. Release builds may
// override it with -ldflags "-X github.com/dorkitude/cfctl/internal/version.Version=...".
var Version = "0.2.710"
