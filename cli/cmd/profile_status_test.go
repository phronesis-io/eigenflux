package cmd

import (
	"strings"
	"testing"
)

// TestStatusPromptAutoPublishFlag guards the C1 fix: the plugin must pass
// --auto-publish=<bool> (equals form). cobra bool flags do NOT consume the next
// token, so the space form `--auto-publish false` silently parses as true and
// would flip an opted-out user into auto-publish. This test pins both the
// equals-form parsing and the NoArgs guard that rejects the stray positional.
func TestStatusPromptAutoPublishFlag(t *testing.T) {
	if err := profileStatusPromptCmd.ParseFlags([]string{"--auto-publish=false"}); err != nil {
		t.Fatalf("parsing --auto-publish=false: %v", err)
	}
	if b, _ := profileStatusPromptCmd.Flags().GetBool("auto-publish"); b {
		t.Errorf("--auto-publish=false must parse as false, got true")
	}
	if err := profileStatusPromptCmd.ParseFlags([]string{"--auto-publish=true"}); err != nil {
		t.Fatalf("parsing --auto-publish=true: %v", err)
	}
	if b, _ := profileStatusPromptCmd.Flags().GetBool("auto-publish"); !b {
		t.Errorf("--auto-publish=true must parse as true, got false")
	}

	// NoArgs must reject the leftover positional the space form would produce.
	if profileStatusPromptCmd.Args == nil {
		t.Fatalf("status-prompt must set Args (cobra.NoArgs)")
	}
	if err := profileStatusPromptCmd.Args(profileStatusPromptCmd, []string{"false"}); err == nil {
		t.Errorf("NoArgs must reject a stray positional arg like the space-form \"false\"")
	}
}

// TestBuildStatusPrompt_AutoPublishBranch verifies the publish-now branch tells
// the agent to publish directly and carries the demand/expected_response shape
// that pulls contributions back, while the draft branch never does.
func TestBuildStatusPrompt_AutoPublishBranch(t *testing.T) {
	memory := []string{"User builds AI agent infra"}
	session := []string{"Shipping a broadcast network"}

	auto := buildStatusPrompt("Vic", "AI infra builder", memory, session, true)
	if !strings.Contains(auto, "publish --content") {
		t.Errorf("auto-publish prompt should carry the `publish --content` command, got:\n%s", auto)
	}
	if !strings.Contains(auto, "expected_response") {
		t.Errorf("auto-publish prompt should include expected_response for UGC pull")
	}
	if !strings.Contains(auto, "recurring_publish is ON") {
		t.Errorf("auto-publish prompt should note recurring_publish ON")
	}

	confirm := buildStatusPrompt("Vic", "AI infra builder", memory, session, false)
	if strings.Contains(confirm, "publish --content") {
		t.Errorf("draft branch must NOT carry the publish command, got:\n%s", confirm)
	}
	if !strings.Contains(confirm, "Do NOT publish") {
		t.Errorf("draft branch should tell the agent not to publish")
	}
	if !strings.Contains(confirm, "confirm") {
		t.Errorf("draft branch should ask the user to confirm")
	}
}

// TestBuildStatusPrompt_ThoughtThenStatusOrder verifies the content ordering
// (thought summary → status/needs) required by the feature.
func TestBuildStatusPrompt_ThoughtThenStatusOrder(t *testing.T) {
	out := buildStatusPrompt("Vic", "bio", []string{"m"}, []string{"s"}, true)
	thought := strings.Index(out, "thought summary")
	status := strings.Index(out, "status update built from that")
	if thought < 0 || status < 0 {
		t.Fatalf("prompt missing thought/status sections:\n%s", out)
	}
	if thought > status {
		t.Errorf("thought summary should come before the status update")
	}
	if !strings.Contains(out, "they now need") {
		t.Errorf("prompt should surface what the user now needs")
	}
}

// TestBuildStatusPrompt_PrivacyRule verifies the privacy hard rule is always
// present regardless of branch.
func TestBuildStatusPrompt_PrivacyRule(t *testing.T) {
	for _, auto := range []bool{true, false} {
		out := buildStatusPrompt("Vic", "bio", []string{"m"}, nil, auto)
		if !strings.Contains(out, "Privacy (hard rule)") {
			t.Errorf("auto=%v: prompt missing privacy hard rule", auto)
		}
	}
}
