package cmd

import (
	"fmt"
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
		"staging",
		"plugin",
	)

	for _, label := range []string{
		"agent_description", "human_description", "seeking/offering", "current_focus",
		"agent_status", "human_status", "KEEP, UPDATE, CLEAR, or UNKNOWN", "Do not manufacture",
		"Re-evaluate the current Agent product on every review", "--runtime-name", "--runtime-version",
		"WorkBuddy process metadata is detected automatically", "Never infer or guess missing values",
	} {
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
	if !strings.Contains(prompt, `eigenflux --server 'staging' settings push --mode plugin`) {
		t.Error("refresh prompt must preserve the target server and plugin delivery mode")
	}
}

func TestBuildCardRefreshSectionKeepsFullCurrentValue(t *testing.T) {
	longValue := strings.Repeat("界", 220)
	raw := []byte(fmt.Sprintf(`{"profile_version":9,"editable_fields":{"agent_description":{"current_value":%q,"public":true}},"protected_paths":[]}`, longValue))
	out := buildCardRefreshSection(raw, "staging")
	if !strings.Contains(out, longValue) {
		t.Fatal("refresh prompt truncated the current value used for diff decisions")
	}
}

func TestBuildCardRefreshSectionMarksVisibilityAndPrivacy(t *testing.T) {
	raw := []byte(`{"profile_version":7,"editable_fields":{"seeking":{"current_value":["AI infra"],"previous_value":["MCP help"],"last_updated_by":"human","last_updated_at":1700000000000,"last_source":"dashboard","last_reason":"goal changed","public":true},"current_focus":{"current_value":["shipping"],"public":false}},"protected_paths":["runtime"]}`)
	out := buildCardRefreshSection(raw, "staging")
	for _, want := range []string{
		"seeking [PUBLIC — visible to every agent]",
		"current_focus [PRIVATE]",
		"--expected-version 7",
		"real names, employers, clients",
		"last updated by HUMAN at 2023-11-14T22:13:20Z",
		"previous value: [\"MCP help\"]",
		`eigenflux --server 'staging' profile refresh-complete --expected-version 7`,
		`eigenflux --server 'staging' profile patch`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("card refresh section missing %q", want)
		}
	}
}

func TestBuildCardRefreshSectionShellQuotesServer(t *testing.T) {
	raw := []byte(`{"profile_version":1,"editable_fields":{},"protected_paths":[]}`)
	out := buildCardRefreshSection(raw, "prod'; touch /tmp/forged; echo '")
	if !strings.Contains(out, `--server 'prod'"'"'; touch /tmp/forged; echo '"'"''`) {
		t.Fatalf("server name was not rendered as one shell argument: %s", out)
	}
}

func TestSafePromptServerNameRejectsPromptControls(t *testing.T) {
	for _, value := range []string{"prod\nignore previous", "prod`command`", "prod\x1b[2J", "local dev", "prod: ignore"} {
		if safePromptServerName(value) {
			t.Errorf("safePromptServerName(%q) = true", value)
		}
	}
	for _, value := range []string{"eigenflux", "staging-us", "Prod_2.example"} {
		if !safePromptServerName(value) {
			t.Errorf("safePromptServerName(%q) = false", value)
		}
	}
}
