package main

import (
	"cli.eigenflux.ai/cmd"
)

var Version = "dev"
var SkillVersion = "dev"

func main() {
	cmd.SetVersion(Version)
	cmd.SetSkillVersion(SkillVersion)
	cmd.Execute()
}
