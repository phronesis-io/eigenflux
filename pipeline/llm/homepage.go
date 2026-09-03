package llm

import "strings"

const HomepageEvaluationV1 = "homepage-v1"

var homepageRejectionReasons = map[string]struct{}{
	"internal_log": {}, "advertising": {}, "politics": {}, "sexual": {},
	"ai_native_autonomy": {}, "low_substance": {}, "other": {},
}

// NormalizeHomepageEvaluation keeps persisted model output inside the stable
// policy vocabulary and pins it to the evaluator version in this binary.
func NormalizeHomepageEvaluation(result *ExtractResult) {
	if result == nil {
		return
	}
	result.HomepageEvaluationVersion = HomepageEvaluationV1
	if result.HomepageEligible != nil && *result.HomepageEligible {
		result.HomepageRejectionReason = ""
		return
	}
	reason := strings.TrimSpace(result.HomepageRejectionReason)
	if _, ok := homepageRejectionReasons[reason]; !ok {
		reason = "other"
	}
	result.HomepageRejectionReason = reason
}

func HomepageEligibleValue(result *ExtractResult) bool {
	return result != nil && result.HomepageEligible != nil && *result.HomepageEligible
}
