package sanity

import (
	"testing"

	commission "eigenflux_server/kitex_gen/eigenflux/commission"
)

func TestCommissionInputFulfillmentSkillRoundTrip(t *testing.T) {
	skill := "repository-security-review"
	input := &commission.CommissionInput{
		Title:                 "Repository security review",
		CapabilityDescription: "Review a bounded repository revision",
		RequestSpecText:       "Provide repository files",
		DeliverySpecText:      "Provide outputs/report.md",
		Tags:                  []string{"security"},
		PriceFen:              1000,
		Currency:              "CNY",
		PromisedDeliveryMs:    86400000,
		RequestSpecSchema:     `{}`,
		DeliverySpecSchema:    `{}`,
		FulfillmentSkill:      &skill,
	}
	encoded := make([]byte, input.BLength())
	input.FastWrite(encoded)

	var decoded commission.CommissionInput
	if _, err := decoded.FastRead(encoded); err != nil {
		t.Fatal(err)
	}
	if decoded.GetFulfillmentSkill() != skill {
		t.Fatalf("fulfillment skill = %q", decoded.GetFulfillmentSkill())
	}
}
