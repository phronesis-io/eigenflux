package cmd

import (
	"strings"
	"testing"
)

// The daily refresh must steer the agent to the same five-part bio structure
// used at onboarding (Domains / Purpose / Recent work / Looking for / Country),
// so the bio stays structured for the server-side keyword extractor instead of
// drifting into free prose over time.
func TestBuildRefreshPromptFivePartFormat(t *testing.T) {
	prompt := buildRefreshPrompt(
		"TestAgent",
		"Domains: ai",
		[]string{"user works on defi and mcp tooling"},
		[]string{"debugging a Go service"},
	)

	for _, label := range []string{"Domains:", "Purpose:", "Recent work:", "Looking for:", "Country:"} {
		if !strings.Contains(prompt, label) {
			t.Errorf("refresh prompt missing five-part section label %q", label)
		}
	}

	// The update command should present the five-part bio template (literal \n
	// separators), not the old free-form "YOUR NEW BIO".
	if !strings.Contains(prompt, `--bio "Domains: ...\nPurpose:`) {
		t.Error("refresh prompt update command should use the five-part bio template")
	}
	if strings.Contains(prompt, "YOUR NEW BIO") {
		t.Error("refresh prompt still uses the old free-form bio placeholder")
	}
}
