package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/output"
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

func TestFeedPendingConfirmationContract(t *testing.T) {
	root := filepath.Join("..", "..")
	var contract, served []byte
	for _, path := range []string{"skills/ef-broadcast/references/contract.md", "skills/ef-broadcast/references/feed.md", "static/feed_contract.md"} {
		body, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		for _, required := range []string{
			"Required profile check-ins and pending-order confirmation reminders are exceptions",
			"After completed-access Feed processing",
			"`eigenflux order pending-confirmations --format json` with the cycle's pinned CLI prefix",
			"Run once per shared Feed/heartbeat cycle, including empty or unchanged Feed",
			"Include pending Orders and their reminder in the owner-facing result every cycle",
			"preserve the host's required output schema and use its existing result field",
			"Never suppress this reminder as already read or confirm receipt automatically",
			"Keep `empty` and `not_enabled` quiet",
			"`not_enabled` means Commission access is unavailable, not that no Orders are pending",
			"Report other failed or unavailable checks without claiming no pending Orders",
			"Skip baseline, maintenance and control runs",
		} {
			if !strings.Contains(string(body), required) {
				t.Errorf("%s missing confirmation boundary %q", path, required)
			}
		}
		if strings.Contains(string(body), "required by step 9 is the sole exception") || strings.Contains(string(body), "required by the phase rules below is the sole exception") {
			t.Errorf("%s retains a conflicting exclusive silence exception", path)
		}
		switch path {
		case "skills/ef-broadcast/references/contract.md":
			contract = body
		case "static/feed_contract.md":
			served = body
		}
	}
	if !bytes.Equal(contract, served) {
		t.Fatal("server-delivered Feed contract differs from the Skill")
	}
	// A bare Agent receives the served rules even with no local Skill loaded
	// and no Feed content. Preserve the payload and the actionable exception.
	payload, err := json.Marshal(map[string]any{
		"personalization": map[string]string{"mode": "intent_aligned"},
		"items":           []any{},
		"output_contract": string(served),
	})
	if err != nil {
		t.Fatal(err)
	}
	var rendered bytes.Buffer
	if err := output.PrintFeedForAgentTo(&rendered, payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered.String(), strings.TrimSpace(string(served))) || !strings.Contains(rendered.String(), `"items": []`) {
		t.Fatal("empty Feed agent output omitted its confirmation rules or payload")
	}
	for _, path := range []string{"skills/ef-broadcast/references/baseline-contract.md", "static/feed_baseline_contract.md"} {
		body, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "pending-confirmations") {
			t.Errorf("%s widens the baseline's read-only scope", path)
		}
	}
}

func TestFeedOwnerOriginAndCompletionContract(t *testing.T) {
	body, err := os.ReadFile("../../skills/ef-broadcast/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "When the owner asks to check or refresh the Feed outside a scheduled cycle, append `--origin owner`") {
		t.Fatal("owner override missing")
	}
	for _, path := range []string{"../../skills/ef-broadcast/SKILL.md", "../../skills/ef-broadcast/references/heartbeat-execution.md"} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "Before leaving the Feed stage, run `<cli_prefix> feed status`. Score every item it lists, then continue.") {
			t.Fatalf("%s missing the completion step", path)
		}
		for _, required := range []string{"feed poll --batch 5 --action refresh", "at most one", "feed next", "After the Feed push, run `<cli_prefix> feed status --after-push`. Report surface only for listed items the owner was actually shown."} {
			if !strings.Contains(string(body), required) {
				t.Errorf("%s missing %q", path, required)
			}
		}
	}
}
