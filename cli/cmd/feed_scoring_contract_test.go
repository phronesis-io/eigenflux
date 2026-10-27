package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFeedScoringFallbackContract(t *testing.T) {
	root := filepath.Join("..", "..")
	var contract []byte
	for _, path := range []string{"skills/ef-broadcast/references/contract.md", "skills/ef-broadcast/references/feed.md", "static/feed_contract.md"} {
		body, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		for _, required := range []string{
			"Use confirmed `intent_actions` as the primary scoring basis when present",
			"When `intent_actions` is null, missing, or empty",
			"`network_goal`, the user profile, current interests, and conversation context",
			"separate from Agent feedback scores",
			"must never independently suppress feedback or recommendations",
			"Submit feedback for every eligible item before presenting Feed recommendations or uploading Feed Attention",
			"Recommend items scored `1` or `2`",
			"Preserve qualified judgments through recoverable feedback errors",
		} {
			if !strings.Contains(string(body), required) {
				t.Errorf("%s missing %q", path, required)
			}
		}
		if path == "skills/ef-broadcast/references/contract.md" {
			contract = body
		}
		if path == "static/feed_contract.md" && string(body) != string(contract) {
			t.Error("served contract differs from Skill")
		}
	}
}
