package dispatch

import "errors"

// CommissionTrialDecision uses the fulfillment evidence contract without inventing
// a real Order or allowing the runner to submit delivery.
type CommissionTrialDecision struct {
	Version      int                  `json:"version"`
	RequestID    string               `json:"request_id"`
	CommissionID string               `json:"commission_id"`
	DraftVersion int64                `json:"draft_version"`
	Outcome      string               `json:"outcome"`
	Summary      string               `json:"summary"`
	SelfCheck    string               `json:"self_check"`
	Artifacts    []CommissionArtifact `json:"artifacts"`
}

func ParseCommissionTrialDecision(raw, requestID, commissionID string, version int64) (CommissionFulfillmentDecision, error) {
	var trial CommissionTrialDecision
	if len(raw) > 1<<20 {
		return CommissionFulfillmentDecision{}, errors.New("commission_trial_result_too_large")
	}
	if err := requiredCommissionFields([]byte(raw), "version", "request_id", "commission_id", "draft_version", "outcome", "summary", "self_check", "artifacts"); err != nil {
		return CommissionFulfillmentDecision{}, err
	}
	if err := decodeStrictObject([]byte(raw), &trial); err != nil {
		return CommissionFulfillmentDecision{}, err
	}
	result := CommissionFulfillmentDecision{Version: trial.Version, RequestID: trial.RequestID, OrderID: trial.CommissionID, OrderVersion: trial.DraftVersion, Outcome: trial.Outcome, Summary: trial.Summary, SelfCheck: trial.SelfCheck, Artifacts: trial.Artifacts}
	return result, validateCommissionFulfillmentDecision(result, requestID, commissionID, version)
}
