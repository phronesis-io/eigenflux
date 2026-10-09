package dispatch

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

type CommissionFulfillmentJob struct {
	Version         int                      `json:"version"`
	OrderID         string                   `json:"order_id"`
	OrderVersion    int64                    `json:"order_version"`
	IntakeRequestID string                   `json:"intake_request_id"`
	IntakeResult    CommissionIntakeDecision `json:"intake_result"`
}

type CommissionArtifact struct {
	LogicalPath  string `json:"logical_path"`
	RelativePath string `json:"relative_path"`
}

type CommissionFulfillmentDecision struct {
	Version      int                  `json:"version"`
	RequestID    string               `json:"request_id"`
	OrderID      string               `json:"order_id"`
	OrderVersion int64                `json:"order_version"`
	Outcome      string               `json:"outcome"`
	Summary      string               `json:"summary"`
	SelfCheck    string               `json:"self_check"`
	Artifacts    []CommissionArtifact `json:"artifacts"`
}

type CommissionVerifiedArtifact struct {
	LogicalPath  string `json:"logical_path"`
	RelativePath string `json:"relative_path"`
	SHA256       string `json:"sha256"`
	ByteSize     int64  `json:"byte_size"`
}

type CommissionFulfillmentResult struct {
	Decision        CommissionFulfillmentDecision `json:"decision"`
	OutputDirectory string                        `json:"output_directory"`
	Artifacts       []CommissionVerifiedArtifact  `json:"artifacts"`
}

func requiredCommissionFields(raw []byte, names ...string) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return errors.New("invalid_commission_fulfillment_contract")
	}
	for _, name := range names {
		if value, ok := fields[name]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("missing_commission_fulfillment_field")
		}
	}
	return nil
}

func (value *CommissionFulfillmentJob) UnmarshalJSON(raw []byte) error {
	type plain CommissionFulfillmentJob
	var decoded plain
	if err := decodeStrictObject(raw, &decoded); err != nil {
		return err
	}
	if err := requiredCommissionFields(raw, "version", "order_id", "order_version", "intake_request_id", "intake_result"); err != nil {
		return err
	}
	if decoded.Version != 1 || decoded.OrderVersion <= 0 || strings.TrimSpace(decoded.IntakeRequestID) == "" {
		return errors.New("invalid_commission_fulfillment_job")
	}
	if _, err := positiveNotificationInteger([]byte(decoded.OrderID)); err != nil {
		return errors.New("invalid_commission_fulfillment_order")
	}
	if decoded.IntakeResult.Outcome != "ready" || validateCommissionIntakeDecision(decoded.IntakeResult, decoded.IntakeRequestID, decoded.OrderID, decoded.OrderVersion) != nil {
		return errors.New("invalid_commission_fulfillment_intake")
	}
	*value = CommissionFulfillmentJob(decoded)
	return nil
}

func (value *CommissionFulfillmentDecision) UnmarshalJSON(raw []byte) error {
	type plain CommissionFulfillmentDecision
	var decoded plain
	if err := decodeStrictObject(raw, &decoded); err != nil {
		return err
	}
	if err := requiredCommissionFields(raw, "version", "request_id", "order_id", "order_version", "outcome", "summary", "self_check", "artifacts"); err != nil {
		return err
	}
	result := CommissionFulfillmentDecision(decoded)
	if err := validateCommissionFulfillmentDecision(result, result.RequestID, result.OrderID, result.OrderVersion); err != nil {
		return err
	}
	*value = result
	return nil
}

func (value *CommissionVerifiedArtifact) UnmarshalJSON(raw []byte) error {
	type plain CommissionVerifiedArtifact
	var decoded plain
	if err := decodeStrictObject(raw, &decoded); err != nil {
		return err
	}
	if err := requiredCommissionFields(raw, "logical_path", "relative_path", "sha256", "byte_size"); err != nil {
		return err
	}
	*value = CommissionVerifiedArtifact(decoded)
	return nil
}

func (value *CommissionFulfillmentResult) UnmarshalJSON(raw []byte) error {
	type plain CommissionFulfillmentResult
	var decoded plain
	if err := decodeStrictObject(raw, &decoded); err != nil {
		return err
	}
	if err := requiredCommissionFields(raw, "decision", "output_directory", "artifacts"); err != nil {
		return err
	}
	*value = CommissionFulfillmentResult(decoded)
	return nil
}

func ParseCommissionFulfillmentDecision(raw, requestID, orderID string, orderVersion int64) (CommissionFulfillmentDecision, error) {
	var result CommissionFulfillmentDecision
	if len(raw) > 1<<20 || !utf8.ValidString(raw) {
		return result, errors.New("invalid_commission_fulfillment_encoding_or_size")
	}
	if err := decodeStrictObject([]byte(raw), &result); err != nil {
		return result, err
	}
	if err := validateCommissionFulfillmentDecision(result, requestID, orderID, orderVersion); err != nil {
		return result, err
	}
	return result, nil
}

func validateCommissionFulfillmentDecision(result CommissionFulfillmentDecision, requestID, orderID string, orderVersion int64) error {
	if strings.TrimSpace(requestID) == "" || result.Version != 1 || result.RequestID != requestID || result.OrderID != orderID || orderVersion <= 0 || result.OrderVersion != orderVersion {
		return errors.New("commission_fulfillment_identity_mismatch")
	}
	if _, err := positiveNotificationInteger([]byte(orderID)); err != nil {
		return errors.New("invalid_commission_fulfillment_order")
	}
	switch result.Outcome {
	case "artifacts_ready", "needs_input", "needs_user", "failed":
	default:
		return errors.New("invalid_commission_fulfillment_outcome")
	}
	if !utf8.ValidString(result.Summary) || strings.TrimSpace(result.Summary) == "" || len(result.Summary) > 2000 || !utf8.ValidString(result.SelfCheck) || len(result.SelfCheck) > 2000 {
		return errors.New("invalid_commission_fulfillment_summary")
	}
	if result.Artifacts == nil || len(result.Artifacts) > 128 || (result.Outcome == "artifacts_ready" && (len(result.Artifacts) == 0 || strings.TrimSpace(result.SelfCheck) == "")) {
		return errors.New("invalid_commission_fulfillment_artifacts")
	}
	logical, relative := map[string]bool{}, map[string]bool{}
	for _, artifact := range result.Artifacts {
		if !validCommissionArtifactPath(artifact.LogicalPath) || !validCommissionArtifactPath(artifact.RelativePath) || logical[artifact.LogicalPath] || relative[artifact.RelativePath] {
			return errors.New("invalid_commission_fulfillment_artifact_path")
		}
		logical[artifact.LogicalPath], relative[artifact.RelativePath] = true, true
	}
	return nil
}

func validCommissionArtifactPath(value string) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || len(value) > 1024 || strings.IndexFunc(value, unicode.IsControl) >= 0 || strings.ContainsAny(value, `\:`) || path.IsAbs(value) || path.Clean(value) != value || value == "." {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

func validCommissionDirectory(value string) bool {
	return utf8.ValidString(value) && len(value) <= 4096 && strings.IndexFunc(value, unicode.IsControl) < 0 && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func commissionFulfillmentID(b Binding, orderID string) string {
	return journalID(b.Scope, b.Revision, "commission_fulfillment", orderID)
}

// HasCommissionFulfillment reports a retained execution record for this binding.
func (j *Journal) HasCommissionFulfillment(orderID string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	id := commissionFulfillmentID(j.binding, orderID)
	for _, job := range j.state.Jobs {
		if job.ID == id && job.Kind == "commission_fulfillment" {
			return true
		}
	}
	return false
}

func (j *Journal) SetCommissionFulfillmentDirectory(id, directory string) error {
	if !validCommissionDirectory(directory) {
		return errors.New("invalid_commission_fulfillment_directory")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	next := cloneJournal(j.state)
	for i := range next.Jobs {
		job := &next.Jobs[i]
		if job.ID != id {
			continue
		}
		if job.Kind != "commission_fulfillment" || job.Status != "running" || (job.CommissionDirectory != "" && job.Code != "operator_retry") {
			return errors.New("commission_fulfillment_directory_not_preparable")
		}
		job.CommissionDirectory = directory
		job.Code = "commission_fulfillment_prepared"
		return j.save(next)
	}
	return errors.New("job_not_found")
}

func (j *Journal) CompleteCommissionFulfillment(id string, result CommissionFulfillmentResult, sessionID string) error {
	return j.saveCommissionFulfillment(id, result, sessionID, false)
}

// BeginCommissionDelivery persists exact artifact evidence before any upload.
func (j *Journal) BeginCommissionDelivery(id string, result CommissionFulfillmentResult, sessionID string) error {
	if result.Decision.Outcome != "artifacts_ready" {
		return errors.New("commission_delivery_requires_artifacts")
	}
	return j.saveCommissionFulfillment(id, result, sessionID, true)
}

func (j *Journal) saveCommissionFulfillment(id string, result CommissionFulfillmentResult, sessionID string, sending bool) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	next := cloneJournal(j.state)
	for i := range next.Jobs {
		job := &next.Jobs[i]
		if job.ID != id {
			continue
		}
		if job.Kind != "commission_fulfillment" || job.Status != "running" || job.Code != "commission_fulfillment_prepared" {
			return errors.New("commission_fulfillment_requires_prepared_job")
		}
		if err := validateCommissionFulfillmentResult(*job, result); err != nil {
			return err
		}
		job.CommissionFulfillment = &result
		job.Status = commissionFulfillmentStatus(result.Decision.Outcome)
		job.Code = "commission_fulfillment_" + result.Decision.Outcome
		if sending {
			job.Status, job.Code = "sending", "commission_delivery_sending"
		}
		if sessionID != "" {
			job.SessionID = sessionID
		}
		return j.saveUpdatedJob(next, i)
	}
	return errors.New("job_not_found")
}

func commissionFulfillmentStatus(outcome string) string {
	if outcome == "failed" {
		return "failed"
	}
	return "needs_user"
}

func validateCommissionFulfillmentResult(job Job, result CommissionFulfillmentResult) error {
	var payload CommissionFulfillmentJob
	if err := json.Unmarshal(job.Data, &payload); err != nil {
		return errors.New("invalid_commission_fulfillment_job")
	}
	if err := validateCommissionFulfillmentDecision(result.Decision, job.ID, payload.OrderID, payload.OrderVersion); err != nil {
		return err
	}
	if !validCommissionDirectory(job.CommissionDirectory) || result.OutputDirectory != job.CommissionDirectory || result.Artifacts == nil || len(result.Artifacts) != len(result.Decision.Artifacts) {
		return errors.New("invalid_commission_fulfillment_evidence")
	}
	wanted := map[string]string{}
	for _, artifact := range result.Decision.Artifacts {
		wanted[artifact.LogicalPath] = artifact.RelativePath
	}
	total := int64(0)
	for _, artifact := range result.Artifacts {
		if relative, ok := wanted[artifact.LogicalPath]; !ok || relative != artifact.RelativePath {
			return errors.New("commission_fulfillment_artifact_mismatch")
		}
		delete(wanted, artifact.LogicalPath)
		digest, err := hex.DecodeString(artifact.SHA256)
		if err != nil || len(digest) != 32 || artifact.ByteSize < 0 || artifact.ByteSize > (64<<20)-total {
			return errors.New("invalid_commission_fulfillment_artifact_evidence")
		}
		total += artifact.ByteSize
	}
	return nil
}

func validCommissionFulfillmentJob(job Job, binding Binding) bool {
	var payload CommissionFulfillmentJob
	if job.Message != nil || json.Unmarshal(job.Data, &payload) != nil || job.ID != commissionFulfillmentID(binding, payload.OrderID) || (job.CommissionDirectory != "" && !validCommissionDirectory(job.CommissionDirectory)) {
		return false
	}
	if job.Code == "commission_fulfillment_prepared" && job.CommissionDirectory == "" {
		return false
	}
	if result := job.CommissionFulfillment; result != nil {
		if validateCommissionFulfillmentResult(job, *result) != nil {
			return false
		}
		if job.Code == "operator_verified" {
			return job.Status == "completed" || job.Status == "failed"
		}
		if result.Decision.Outcome == "artifacts_ready" {
			switch job.Code {
			case "commission_delivery_sending":
				return job.Status == "sending"
			case "commission_delivery_confirmed":
				return job.Status == "completed"
			case "commission_delivery_unconfirmed", "interrupted_execution":
				return job.Status == "unknown"
			}
		}
		return job.Status == commissionFulfillmentStatus(result.Decision.Outcome) && job.Code == "commission_fulfillment_"+result.Decision.Outcome
	}
	return true
}
