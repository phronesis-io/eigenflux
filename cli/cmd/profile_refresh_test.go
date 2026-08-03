package cmd

import (
	"strings"
	"testing"
)

// The daily refresh must use the versioned field-level flow, never the legacy
// whole-bio update that can overwrite unrelated human edits.
func TestBuildRefreshPromptFivePartFormat(t *testing.T) {
	prompt := buildRefreshPrompt(
		"TestAgent",
		"Domains: ai",
		[]string{"user works on defi and mcp tooling"},
		[]string{"debugging a Go service"},
	)

	for _, label := range []string{"agent_description", "human_description", "seeking/offering", "current_focus"} {
		if !strings.Contains(prompt, label) {
			t.Errorf("refresh prompt missing field-level guidance %q", label)
		}
	}
	if !strings.Contains(prompt, "Never use legacy `eigenflux profile update`") {
		t.Error("refresh prompt must explicitly prohibit legacy whole-profile writes")
	}
	if strings.Contains(prompt, `--bio "`) {
		t.Error("refresh prompt still contains a legacy whole-bio write command")
	}
}

func TestBuildCardRefreshSectionMarksVisibilityAndPrivacy(t *testing.T) {
	raw := []byte(`{"profile_version":7,"editable_fields":{"seeking":{"current_value":["AI infra"],"public":true},"current_focus":{"current_value":["shipping"],"public":false}},"protected_paths":["runtime"]}`)
	out := buildCardRefreshSection(raw)
	for _, want := range []string{
		"seeking [PUBLIC — visible to every agent]",
		"current_focus [PRIVATE]",
		"--expected-version 7",
		"real names, employers, clients",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("card refresh section missing %q", want)
		}
	}
}
