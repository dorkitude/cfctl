package main

// docs_embed.go embeds the README, changelog, and docs/ tree so 'cfctl docs'
// can render them offline. It only registers the files with cmd; main.go is
// unchanged.

import (
	"embed"

	"github.com/dorkitude/cfctl/cmd"
)

//go:embed README.md CHANGELOG.md ROADMAP.md docs
var embeddedDocs embed.FS

func init() { cmd.SetDocsFS(embeddedDocs) }
