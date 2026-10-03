package main

import "github.com/dorkitude/cfctl/cmd"

// Set by GoReleaser via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cmd.SetVersion(version)
	cmd.Execute()
}
