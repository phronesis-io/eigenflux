package workbuddy

import (
	"strings"
	"testing"
)

func TestDefaultAllowedToolsMatchHostShell(t *testing.T) {
	for _, platform := range []string{"windows", "darwin", "linux"} {
		want := "Read,Write,Edit,Bash"
		if platform == "windows" {
			want += ",PowerShell"
		}
		if got := strings.Join(defaultAllowedTools(platform), ","); got != want {
			t.Fatalf("%s: %s", platform, got)
		}
	}
	tools := defaultAllowedTools("windows")
	tools[0] = "mutated"
	if defaultAllowedTools("windows")[0] != "Read" {
		t.Fatal("shared mutable allow rules")
	}
}
