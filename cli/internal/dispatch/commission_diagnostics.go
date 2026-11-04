package dispatch

import (
	"cli.eigenflux.ai/internal/maintenance"
	"encoding/json"
	"errors"
	"strconv"
)

type CommissionDiagnostic struct {
	BridgeError      *maintenance.CommissionBridgeError  `json:"bridge_error,omitempty"`
	CheckReceipt     *maintenance.CommissionCheckReceipt `json:"check_receipt,omitempty"`
	SnapshotReported string                              `json:"snapshot_reported,omitempty"`
}
type CommissionDiagnosticSnapshot struct {
	Observation maintenance.Commission
	Status      string
	Key         string
}

func cloneCommissionDiagnostic(d *CommissionDiagnostic) *CommissionDiagnostic {
	if d == nil {
		return nil
	}
	copy := *d
	if d.BridgeError != nil {
		v := *d.BridgeError
		if v.ExitCode != nil {
			x := *v.ExitCode
			v.ExitCode = &x
		}
		if v.RPCCode != nil {
			x := *v.RPCCode
			v.RPCCode = &x
		}
		copy.BridgeError = &v
	}
	if d.CheckReceipt != nil {
		v := *d.CheckReceipt
		if v.InspectedFileCount != nil {
			x := *v.InspectedFileCount
			v.InspectedFileCount = &x
		}
		copy.CheckReceipt = &v
	}
	return &copy
}
func validCommissionDiagnostic(d *CommissionDiagnostic) bool {
	return d == nil || ((d.SnapshotReported == "" || executionReceiptPattern.MatchString(d.SnapshotReported)) && maintenance.ValidateCommissionBridgeError(d.BridgeError) == nil && maintenance.ValidateCommissionCheckReceipt(d.CheckReceipt) == nil)
}

// Metadata persistence never changes execution status, readiness or retries.
func (j *Journal) SaveCommissionDiagnostic(id string, bridge *maintenance.CommissionBridgeError, check *maintenance.CommissionCheckReceipt) error {
	if maintenance.ValidateCommissionBridgeError(bridge) != nil || maintenance.ValidateCommissionCheckReceipt(check) != nil {
		return errors.New("invalid_commission_diagnostic")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	next := cloneJournal(j.state)
	for i := range next.Jobs {
		job := &next.Jobs[i]
		if job.ID != id {
			continue
		}
		if job.Kind != "commission_order" && job.Kind != "commission_fulfillment" && job.Kind != "commission_clarification" {
			return errors.New("invalid_commission_diagnostic_kind")
		}
		if job.CommissionDiagnostic == nil {
			job.CommissionDiagnostic = &CommissionDiagnostic{}
		}
		if bridge != nil {
			job.CommissionDiagnostic.BridgeError = cloneCommissionDiagnostic(&CommissionDiagnostic{BridgeError: bridge}).BridgeError
		}
		if check != nil {
			job.CommissionDiagnostic.CheckReceipt = cloneCommissionDiagnostic(&CommissionDiagnostic{CheckReceipt: check}).CheckReceipt
		}
		return j.save(next)
	}
	return errors.New("job_not_found")
}
func CheckReceipt(result CommissionIntakeDecision, fingerprint string) *maintenance.CommissionCheckReceipt {
	if validateCommissionIntakeDecision(result, result.RequestID, result.OrderID, result.OrderVersion) != nil || !executionReceiptPattern.MatchString(result.RequestID) {
		return nil
	}
	count := len(result.InspectedFiles)
	receipt := &maintenance.CommissionCheckReceipt{Status: "present", Outcome: result.Outcome, CheckedOrderVersion: result.OrderVersion, InspectedFileCount: &count, SourceJobID: result.RequestID}
	if result.Outcome == "ready" && validCommissionInputFingerprint(fingerprint) {
		receipt.InputFingerprint = fingerprint
	}
	if maintenance.ValidateCommissionCheckReceipt(receipt) != nil {
		return nil
	}
	return receipt
}
func ownCommissionCheck(job Job) *maintenance.CommissionCheckReceipt {
	if job.CommissionReadiness != nil {
		if value := CheckReceipt(job.CommissionReadiness.Result, job.CommissionReadiness.InputFingerprint); value != nil {
			return value
		}
	}
	if d := job.CommissionDiagnostic; d != nil && d.CheckReceipt != nil {
		return cloneCommissionDiagnostic(d).CheckReceipt
	}
	if job.CommissionResult != nil {
		if value := CheckReceipt(*job.CommissionResult, ""); value != nil {
			return value
		}
	}
	if job.Kind == "commission_fulfillment" {
		var data CommissionFulfillmentJob
		if json.Unmarshal(job.Data, &data) == nil && data.IntakeResult.OrderID == data.OrderID && data.IntakeResult.RequestID == data.IntakeRequestID {
			return CheckReceipt(data.IntakeResult, data.InputFingerprint)
		}
	}
	return nil
}

// Extract bounded fields under the journal lock. Never return private Data,
// model summaries, inspected file paths or process output to telemetry consumers.
func commissionDiagnosticObservation(job Job, jobs []Job) maintenance.Commission {
	c := maintenance.Commission{JobID: job.ID, JobKind: job.Kind, Role: "seller", Attempt: 1, LocalCode: maintenance.CommissionLocalCode(job.Code)}
	var data struct {
		OrderID      json.RawMessage `json:"order_id"`
		OrderVersion int64           `json:"order_version"`
		Notification json.RawMessage `json:"notification_id"`
		Payload      struct {
			OrderID json.RawMessage `json:"order_id"`
			Version int64           `json:"order_version"`
			Role    string          `json:"recipient_role"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(job.Data, &data)
	if id, err := positiveNotificationInteger(data.OrderID); err == nil {
		c.OrderID = id
		c.OrderVersion = data.OrderVersion
	}
	if id, err := positiveNotificationInteger(data.Payload.OrderID); err == nil {
		c.OrderID = id
		c.OrderVersion = data.Payload.Version
		if data.Payload.Role == "buyer" {
			c.Role = "buyer"
		}
	}
	if id, err := positiveNotificationInteger(data.Notification); err == nil {
		c.SourceNotificationID = id
	}
	if job.Kind == "commission_clarification" {
		c.Role = "buyer"
	}
	if job.CommissionExecution != nil {
		c.OrderID = job.CommissionExecution.OrderID
		c.OrderVersion = job.CommissionExecution.OrderVersion
	}
	c.CheckReceipt = ownCommissionCheck(job)
	if c.OrderID == "" && job.CommissionReadiness != nil {
		c.OrderID = job.CommissionReadiness.Result.OrderID
		c.OrderVersion = job.CommissionReadiness.Result.OrderVersion
	}
	if c.OrderID == "" && job.CommissionResult != nil {
		c.OrderID = job.CommissionResult.OrderID
		c.OrderVersion = job.CommissionResult.OrderVersion
	}
	if c.CheckReceipt == nil && job.Kind == "commission_order" {
		for _, source := range jobs {
			if source.ID == job.ID {
				continue
			}
			receipt := ownCommissionCheck(source)
			if commissionDiagnosticOrderID(source) != c.OrderID || receipt == nil || receipt.CheckedOrderVersion > c.OrderVersion || (receipt.Outcome != "ready" && receipt.CheckedOrderVersion != c.OrderVersion) {
				continue
			}
			if c.CheckReceipt == nil || receipt.CheckedOrderVersion > c.CheckReceipt.CheckedOrderVersion {
				c.CheckReceipt = receipt
			}
		}
	}
	if c.CheckReceipt == nil {
		status := "not_applicable"
		if job.Kind == "commission_order" || job.Kind == "commission_fulfillment" {
			status = "missing"
		}
		c.CheckReceipt = &maintenance.CommissionCheckReceipt{Status: status}
	}
	c.BridgeError = &maintenance.CommissionBridgeError{Code: "unknown_failure"}
	if job.CommissionDiagnostic != nil && job.CommissionDiagnostic.BridgeError != nil {
		c.BridgeError = cloneCommissionDiagnostic(job.CommissionDiagnostic).BridgeError
	}
	return c
}
func commissionDiagnosticOrderID(job Job) string {
	if job.CommissionExecution != nil {
		return job.CommissionExecution.OrderID
	}
	if job.CommissionReadiness != nil {
		return job.CommissionReadiness.Result.OrderID
	}
	if job.CommissionResult != nil {
		return job.CommissionResult.OrderID
	}
	var data struct {
		OrderID string `json:"order_id"`
	}
	_ = json.Unmarshal(job.Data, &data)
	return data.OrderID
}
func (j *Journal) CommissionDiagnosticObservation(id string) (maintenance.Commission, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, job := range j.state.Jobs {
		if job.ID == id {
			return commissionDiagnosticObservation(job, j.state.Jobs), true
		}
	}
	return maintenance.Commission{}, false
}
func (j *Journal) CommissionDiagnosticSnapshots(limit int) []CommissionDiagnosticSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	result := []CommissionDiagnosticSnapshot{}
	if limit <= 0 {
		return result
	}
	limit = min(limit, 32)
	for _, job := range j.state.Jobs {
		if job.Kind != "commission_order" && job.Kind != "commission_fulfillment" && job.Kind != "commission_clarification" {
			continue
		}
		if job.Status != "needs_user" && job.Status != "failed" && job.Status != "unknown" {
			continue
		}
		c := commissionDiagnosticObservation(job, j.state.Jobs)
		if id, err := strconv.ParseInt(c.OrderID, 10, 64); err != nil || id <= 0 {
			continue
		}
		c.Stage = "diagnostic_snapshot"
		c.Attempt = 0
		raw, _ := json.Marshal(c)
		key := journalID("commission_diagnostic_snapshot.v1", job.Status, string(raw))
		if job.CommissionDiagnostic != nil && job.CommissionDiagnostic.SnapshotReported == key {
			continue
		}
		result = append(result, CommissionDiagnosticSnapshot{Observation: c, Status: job.Status, Key: key})
		if len(result) == limit {
			break
		}
	}
	return result
}
func (j *Journal) AcknowledgeCommissionDiagnosticSnapshot(id, key string) error {
	if !executionReceiptPattern.MatchString(key) {
		return errors.New("invalid_commission_diagnostic_key")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	next := cloneJournal(j.state)
	for i := range next.Jobs {
		job := &next.Jobs[i]
		if job.ID != id {
			continue
		}
		if job.Status != "failed" && job.Status != "unknown" && job.Status != "needs_user" {
			return nil
		}
		observation := commissionDiagnosticObservation(*job, next.Jobs)
		observation.Stage = "diagnostic_snapshot"
		observation.Attempt = 0
		raw, _ := json.Marshal(observation)
		if key != journalID("commission_diagnostic_snapshot.v1", job.Status, string(raw)) {
			return nil
		}
		if job.CommissionDiagnostic == nil {
			job.CommissionDiagnostic = &CommissionDiagnostic{}
		}
		job.CommissionDiagnostic.SnapshotReported = key
		return j.save(next)
	}
	return nil
}
