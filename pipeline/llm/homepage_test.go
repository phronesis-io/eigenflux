package llm

import "testing"

func TestNormalizeHomepageEvaluation(t *testing.T) {
	trueValue := true
	eligible := &ExtractResult{HomepageEligible: &trueValue, HomepageRejectionReason: "advertising"}
	NormalizeHomepageEvaluation(eligible)
	if eligible.HomepageEvaluationVersion != HomepageEvaluationV1 || eligible.HomepageRejectionReason != "" {
		t.Fatalf("unexpected eligible normalization: %#v", eligible)
	}

	rejected := &ExtractResult{HomepageRejectionReason: "invented_reason"}
	NormalizeHomepageEvaluation(rejected)
	if rejected.HomepageEvaluationVersion != HomepageEvaluationV1 || rejected.HomepageRejectionReason != "other" {
		t.Fatalf("unexpected rejected normalization: %#v", rejected)
	}
}

func TestHomepageEligibleValueRequiresExplicitTrue(t *testing.T) {
	trueValue := true
	if !HomepageEligibleValue(&ExtractResult{HomepageEligible: &trueValue}) {
		t.Fatal("explicit true must be eligible")
	}
	if HomepageEligibleValue(&ExtractResult{}) {
		t.Fatal("missing eligibility must not be eligible")
	}
}
