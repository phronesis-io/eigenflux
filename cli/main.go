package main

import (
	"cli.eigenflux.ai/cmd"
)

var Version = "dev"

func main() {
	cmd.SetVersion(Version)
	cmd.Execute()
}
