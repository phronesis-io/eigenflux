package llm

import "testing"

func TestNormalizeHomepageEvaluation(t *testing.T) {
	trueValue := true
	eligible := &ExtractResult{HomepageEligible: &trueValue, HomepageRejectionReason: "advertising"}
	NormalizeHomepageEvaluation(eligible)
	if eligible.HomepageEvaluationVersion != HomepageEvaluationV2 || eligible.HomepageRejectionReason != "" {
		t.Fatalf("unexpected eligible normalization: %#v", eligible)
	}

	rejected := &ExtractResult{HomepageRejectionReason: "invented_reason"}
	NormalizeHomepageEvaluation(rejected)
	if rejected.HomepageEvaluationVersion != HomepageEvaluationV2 || rejected.HomepageRejectionReason != "other" {
		t.Fatalf("unexpected rejected normalization: %#v", rejected)
	}

	incomplete := &ExtractResult{HomepageEligible: &trueValue, HomepageEvaluationIncomplete: true}
	NormalizeHomepageEvaluation(incomplete)
	if incomplete.HomepageEvaluationVersion != "" {
		t.Fatalf("incomplete evaluation version = %q, want empty for backfill", incomplete.HomepageEvaluationVersion)
	}
}

func TestHomepageRealWorldRelevantValueRequiresExplicitTrue(t *testing.T) {
	trueValue := true
	if !HomepageRealWorldRelevantValue(&ExtractResult{HomepageRealWorldRelevant: &trueValue}) {
		t.Fatal("explicit true must be real-world relevant")
	}
	if HomepageRealWorldRelevantValue(&ExtractResult{}) {
		t.Fatal("missing relevance must not be real-world relevant")
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
