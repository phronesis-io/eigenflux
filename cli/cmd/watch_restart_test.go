package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestWatchRestartRequiresExactlyOneExplicitTarget(t *testing.T) {
	for _, args := range [][]string{nil, {"--launch-agent", "/tmp/one.plist", "--scheduled-task", `\OtherTask`}} {
		command := newWatchRestartCommand()
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs(args)
		if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Fatalf("args=%v error=%v", args, err)
		}
	}
}
