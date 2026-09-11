package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommissionSkillContract(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	skillBody, err := os.ReadFile(filepath.Join(repoRoot, "skills/ef-commission/SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(skillBody), `version: "999.0.0-dev.20260910"`) {
		t.Error("ef-commission version is stale")
	}
	commissionBody, err := os.ReadFile(filepath.Join(repoRoot, "skills/ef-commission/references/commission.md"))
	if err != nil {
		t.Fatal(err)
	}
	commission := string(commissionBody)
	for _, required := range []string{
		"one question at a time",
		"canonical input manifest",
		"fixed delivery manifest",
		"buyer-specific inputs and secrets",
		"eigenflux skills path",
		"frontmatter `name`",
		"--fulfillment-skill repository-security-review",
		"--commission-id COMMISSION_ID",
		"exactly one of `--query` or `--commission-id`",
		"eigenflux commission orderable COMMISSION_ID --format json",
		"only the current orderable published",
		"eigenflux commission save COMMISSION_ID",
		"eigenflux commission saved --limit 20",
		"eigenflux commission unsave COMMISSION_ID",
		"offline Commission remains in the saved list",
		"missing required input",
		"Read back the complete Commission",
		"Current CLI and service validation are authoritative",
	} {
		if !strings.Contains(commission, required) {
			t.Errorf("Commission listing workflow is missing %q", required)
		}
	}

	orderBody, err := os.ReadFile(filepath.Join(repoRoot, "skills/ef-commission/references/order.md"))
	if err != nil {
		t.Fatal(err)
	}
	order := string(orderBody)
	for _, required := range []string{
		"frozen `fulfillment_skill`",
		"before accepting",
		"structured buyer input",
		"declared workspace files",
		"fixed logical path",
		"receipt or summary",
		"Do not accept",
		"Current CLI and service validation are authoritative",
	} {
		if !strings.Contains(order, required) {
			t.Errorf("seller Order workflow is missing %q", required)
		}
	}
}
