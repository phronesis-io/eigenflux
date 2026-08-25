package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttentionSkillConsumesHumanResponsesBeforeFeed(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	skillBody, err := os.ReadFile(filepath.Join(repoRoot, "skills/ef-broadcast/SKILL.md"))
	if err != nil {
		t.Fatalf("read ef-broadcast skill: %v", err)
	}
	skill := string(skillBody)
	commandStep := strings.Index(skill, "1. **Commands**")
	feedStep := strings.Index(skill, "2. **Feed**")
	if commandStep < 0 || feedStep < 0 || commandStep >= feedStep {
		t.Fatal("ef-broadcast heartbeat must process durable commands before Feed")
	}
	for _, required := range []string{
		"after completed onboarding",
		"claim, handle, and complete every `attention_response`",
		"`references/attention.md`",
	} {
		if !strings.Contains(skill, required) {
			t.Errorf("ef-broadcast heartbeat is missing %q", required)
		}
	}

	referenceBody, err := os.ReadFile(filepath.Join(repoRoot, "skills/ef-broadcast/references/attention.md"))
	if err != nil {
		t.Fatalf("read Attention reference: %v", err)
	}
	reference := string(referenceBody)
	for _, required := range []string{
		"before Feed on every heartbeat",
		"eigenflux context pull --format json",
		"eigenflux runtime heartbeat --format json",
		"eigenflux runtime command pending --limit 20 --format json",
		"eigenflux runtime command claim --command-id COMMAND_ID --format json",
		"eigenflux runtime command complete --command-id COMMAND_ID",
		"--claim-token CLAIM_TOKEN",
		"--claim-epoch CLAIM_EPOCH",
		"--status completed",
		"--status failed",
		"Every successful claim must reach `completed` or `failed` in the same cycle",
		"Do not act or complete when claim fails, expires, or is fenced",
	} {
		if !strings.Contains(reference, required) {
			t.Errorf("Attention response loop is missing %q", required)
		}
	}
}

func TestAttentionSkillRuntimeCommandsExist(t *testing.T) {
	commands := []struct{ name, got, want string }{
		{name: "context pull", got: contextV2PullCmd.Use, want: "pull"},
		{name: "runtime heartbeat", got: runtimeV2HeartbeatCmd.Use, want: "heartbeat"},
		{name: "runtime command pending", got: runtimeV2CommandPendingCmd.Use, want: "pending"},
		{name: "runtime command claim", got: runtimeV2CommandClaimCmd.Use, want: "claim"},
		{name: "runtime command complete", got: runtimeV2CommandCompleteCmd.Use, want: "complete"},
	}

	for _, command := range commands {
		if command.got != command.want {
			t.Errorf("%s command Use = %q, want %q", command.name, command.got, command.want)
		}
	}
	for _, flag := range []string{"limit"} {
		if runtimeV2CommandPendingCmd.Flags().Lookup(flag) == nil {
			t.Errorf("runtime command pending is missing --%s", flag)
		}
	}
	for _, flag := range []string{"command-id"} {
		if runtimeV2CommandClaimCmd.Flags().Lookup(flag) == nil {
			t.Errorf("runtime command claim is missing --%s", flag)
		}
	}
	for _, flag := range []string{"command-id", "claim-token", "claim-epoch", "status", "result"} {
		if runtimeV2CommandCompleteCmd.Flags().Lookup(flag) == nil {
			t.Errorf("runtime command complete is missing --%s", flag)
		}
	}
	if rootCmd.PersistentFlags().Lookup("format") == nil {
		t.Error("eigenflux root command is missing --format")
	}
}
