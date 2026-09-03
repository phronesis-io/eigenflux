package llm

import "testing"

func TestNormalizeHomepageEvaluation(t *testing.T) {
	eligible := &ExtractResult{HomepageEligible: true, HomepageRejectionReason: "advertising"}
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
