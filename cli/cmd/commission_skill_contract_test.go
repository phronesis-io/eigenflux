package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommissionSkillRequiresReusableFulfillmentSkill(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	skillBody, err := os.ReadFile(filepath.Join(repoRoot, "skills/ef-commission/SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(skillBody), `version: "0.3.0"`) {
		t.Error("ef-commission version was not advanced for fulfillment-skill binding")
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
		"missing required input",
		"Read back the complete Commission",
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
	} {
		if !strings.Contains(order, required) {
			t.Errorf("seller Order workflow is missing %q", required)
		}
	}
}
