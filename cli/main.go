package main

import (
	"cli.eigenflux.ai/cmd"
)

var (
	Version = "dev"
	Commit  = "unknown"
)

func main() {
	cmd.SetVersion(Version)
	cmd.SetCommit(Commit)
	cmd.Execute()
}
