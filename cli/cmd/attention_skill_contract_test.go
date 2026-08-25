package cmd

import (
	"encoding/json"
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
		"one-hour scheduled cycle is a cadence recommendation, not an admission rule",
		"20 total items, 4 `participation` items, and 16 `focus` items per Agent per rolling 60 minutes",
		"Honor `retry_after_seconds`",
		"run `pending` again before Feed",
		"shell-quoted as exactly one `--result` value",
		"requires a concise `summary`",
		"may include `related_entities`",
		"Include at most 5 related entities",
		"requires a stable `type` and EigenFlux-issued `id`",
		"`agent`, `broadcast`, `broadcast_reply`, `friend_request`, `relation`, `private_message`, `network_goal`, `intent`, or `activity`",
		"`label` and `url` are optional",
		"same-origin relative route",
		"external, local, private-network, internal",
		"ticket, nonce, and token URLs",
		"Every successful claim must reach `completed` or `failed` in the same cycle",
		"Do not act or complete when claim fails, expires, or is fenced",
	} {
		if !strings.Contains(reference, required) {
			t.Errorf("Attention response loop is missing %q", required)
		}
	}
}

func TestRuntimeCompleteAcceptsAttentionResultContract(t *testing.T) {
	result, err := parseRuntimeCommandResult(`{"summary":"已完成处理","related_entities":[{"type":"broadcast","id":"123","label":"相关广播","url":"/dashboard/broadcasts/123"}]}`)
	if err != nil {
		t.Fatalf("valid Attention completion result rejected: %v", err)
	}
	if result["summary"] != "已完成处理" {
		t.Fatalf("unexpected summary: %#v", result["summary"])
	}
	entities, ok := result["related_entities"].([]interface{})
	if !ok || len(entities) != 1 {
		t.Fatalf("unexpected related_entities: %#v", result["related_entities"])
	}
	encoded, err := json.Marshal(result)
	if err != nil || !json.Valid(encoded) {
		t.Fatalf("Attention completion result is not a JSON object: %s (%v)", encoded, err)
	}
	for _, invalid := range []string{"null", "[]", "not-json"} {
		if _, err := parseRuntimeCommandResult(invalid); err == nil {
			t.Errorf("invalid --result %q was accepted", invalid)
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
