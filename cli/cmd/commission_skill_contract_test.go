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
	if !strings.Contains(string(skillBody), `version: "999.0.27-dev.20261027"`) {
		t.Error("ef-commission version is stale")
	}
	skill := string(skillBody)
	for _, required := range []string{
		"## Funding Channel",
		"Payments and payouts currently support Alipay in CNY only",
		"never as a separate qualification to raise up front",
		"read `eigenflux wallet get --format json` before the interview",
		"Do not repeat it for later Orders, or for a buyer who has already paid one",
		"Never offer another payment or payout channel, promise future support, or state a timeline",
		"Establish it from Wallet state or an already-paid Order rather than repeating it every time",
	} {
		if !strings.Contains(skill, required) {
			t.Errorf("funding channel disclosure is missing %q", required)
		}
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
		"eigenflux commission recent --limit 20",
		"missing required input",
		"Read back the complete Commission",
		"Current CLI and service validation are authoritative",
		"## Confirm Payout Eligibility First",
		"A bound Alipay account already settles this; continue without raising it",
		"rather than starting preparatory work that could not be paid out",
		"Skip it for a buyer who has already paid an Order",
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
		"before doing fulfillment work",
		"structured buyer input",
		"declared workspace files",
		"fixed logical path",
		"receipt or summary",
		"Do not perform fulfillment",
		"Current CLI and service validation are authoritative",
		"eigenflux stream --once",
		"availability fact",
		"Orders are payable through Alipay in CNY only",
		"Do not restate the limit for later Orders or for a buyer who has already paid one",
		"unless this conversation already established it",
	} {
		if !strings.Contains(order, required) {
			t.Errorf("seller Order workflow is missing %q", required)
		}
	}

	walletBody, err := os.ReadFile(filepath.Join(repoRoot, "skills/ef-commission/references/wallet.md"))
	if err != nil {
		t.Fatal(err)
	}
	wallet := string(walletBody)
	for _, required := range []string{
		"Payouts currently support Alipay in CNY only",
		"withdrawal requires that account to pass the identity comparison below",
		"State this once when the seller first reaches earnings, binding, or withdrawal",
		"never offer or promise another payout channel",
		"The only bindable payout account is an Alipay account",
	} {
		if !strings.Contains(wallet, required) {
			t.Errorf("Wallet payout disclosure is missing %q", required)
		}
	}
}

func TestCommissionInteractiveFulfillmentReusesIntakeInspection(t *testing.T) {
	body, err := os.ReadFile("../../skills/ef-commission/references/order.md")
	if err != nil {
		t.Fatal(err)
	}
	order := string(body)
	intakeStart := strings.Index(order, "## Seller Intake and Acceptance")
	fulfillmentStart := strings.Index(order, "## Seller Fulfillment Uses the Frozen Skill")
	fulfillmentEnd := strings.Index(order, "## Versioned Mutations")
	if intakeStart < 0 || fulfillmentStart <= intakeStart || fulfillmentEnd <= fulfillmentStart {
		t.Fatal("seller intake and fulfillment sections are missing or reordered")
	}
	intake := order[intakeStart:fulfillmentStart]
	for _, required := range []string{
		"For seller Orders in `awaiting_seller`",
		"inspect every declared input's actual format and contents",
		"Return `ready` only after inspection",
	} {
		if !strings.Contains(intake, required) {
			t.Errorf("order-entry inspection is missing %q", required)
		}
	}
	fulfillment := order[fulfillmentStart:fulfillmentEnd]
	for _, required := range []string{
		"do not repeat its readiness assessment before paid fulfillment",
		"frontmatter name exactly matches the frozen identifier",
		"verify their size and SHA-256 against the sealed input manifest",
		"Stop on unavailable or changed inputs",
		"After verified payment reaches `in_progress`",
		"validate the bytes and observable acceptance criteria",
	} {
		if !strings.Contains(fulfillment, required) {
			t.Errorf("paid fulfillment contract is missing %q", required)
		}
	}
	for _, obsolete := range []string{
		"Before fulfillment, validate every structured buyer input",
		"inspect their actual formats and contents",
	} {
		if strings.Contains(fulfillment, obsolete) {
			t.Errorf("paid fulfillment repeats intake inspection: %q", obsolete)
		}
	}
}
