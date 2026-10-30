package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommissionPublishPermissionPreflightContract(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "skills", "ef-commission", "references", "commission.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	check := strings.Index(text, "eigenflux notify status --publish-preflight --format json")
	approval := strings.Index(text, "After explicit approval")
	publish := strings.Index(text, "eigenflux commission publish COMMISSION_ID")
	create := strings.Index(text, "eigenflux commission create")
	if check < 0 || create <= check || approval <= check || publish <= check {
		t.Fatal("independent notification check must precede creation, publish approval and execution")
	}
	for _, required := range []string{"on the seller's computer", "`warning`", "`settings_url`", "`settings_path`", "`unknown` distinct from `permission_denied`", "Preserve existing publish approval", "publish stderr guidance as a fallback"} {
		if !strings.Contains(text, required) {
			t.Errorf("publish preflight contract missing %q", required)
		}
	}
	status, _, err := newNotifyCommand().Find([]string{"status"})
	if err != nil || status.Name() != "status" || status.Flags().Lookup("publish-preflight") == nil {
		t.Fatal("Skill preflight command is not registered")
	}
	enabled, err := status.Flags().GetBool("publish-preflight")
	if err != nil || enabled {
		t.Fatal("existing notify status must retain its default behavior")
	}
}

func TestCommissionWorkBuddyBackgroundSetupContract(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "skills", "ef-commission", "references", "commission.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, required := range []string{"With existing publication approval", "current Home/server", "watch workbuddy-acp --node ABSOLUTE_NODE", "application-data workspace", "account-owned user-level service", "watch --dispatch", "pending jobs", "host permission checks enabled", "OAuth credentials untouched", "independently of a main conversation or GUI refresh", "reject stale test bindings", "real publish trial"} {
		if !strings.Contains(text, required) {
			t.Errorf("background setup contract missing %q", required)
		}
	}
}
