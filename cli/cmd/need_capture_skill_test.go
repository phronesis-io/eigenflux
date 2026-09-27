package cmd

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNeedCaptureMaintenanceSkillContract(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	procedure := readRepoFile(t, root, "skills/ef-broadcast/references/needs.md")
	for _, required := range []string{"need capture pending --limit 2", "60 seconds", "existing_inputs", "need capture complete --file", "no_need", "INTENT_REVISION_STALE", "IDEMPOTENCY_CONFLICT", "exact Intent version"} {
		if !strings.Contains(procedure, required) {
			t.Errorf("maintenance contract lacks %q", required)
		}
	}
	for _, path := range []string{"skills/ef-broadcast/SKILL.md", "skills/ef-broadcast/references/feed.md", "skills/ef-broadcast/references/baseline-contract.md"} {
		body := readRepoFile(t, root, path)
		if !strings.Contains(body, "completed onboarding") || !strings.Contains(body, "`need_capture`") {
			t.Errorf("%s cannot continue capture after completed-onboarding baseline", path)
		}
	}
	if body := readRepoFile(t, root, "skills/ef-profile/SKILL.md"); !strings.Contains(body, "needs.md#automatic-maintenance") {
		t.Fatal("Intent edit route does not trigger capture")
	}
}
