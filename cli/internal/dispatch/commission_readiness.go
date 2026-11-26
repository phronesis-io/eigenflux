package dispatch

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
)

var ErrCommissionReadinessCapacity = errors.New("commission_readiness_capacity_exceeded")

func validCommissionInputFingerprint(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && value == hex.EncodeToString(raw)
}

func validCommissionReadiness(value CommissionReadiness) bool {
	result := value.Result
	return result.Outcome == "ready" && validCommissionInputFingerprint(value.InputFingerprint) && validateCommissionIntakeDecision(result, result.RequestID, result.OrderID, result.OrderVersion) == nil
}

// CommissionReworkReadiness exposes only the original check, not private job data.
func (j *Journal) CommissionReworkReadiness(orderID string, version int64) (string, *CommissionReadiness) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, job := range j.state.Jobs {
		if job.Kind != "commission_fulfillment" {
			continue
		}
		var previous CommissionFulfillmentJob
		if json.Unmarshal(job.Data, &previous) != nil || previous.OrderID != orderID || previous.OrderVersion >= version || previous.DeliveryReview != nil {
			continue
		}
		readiness := CommissionReadiness{Result: previous.IntakeResult, InputFingerprint: previous.InputFingerprint}
		if !validCommissionReadiness(readiness) {
			continue
		}
		readiness.Result.InspectedFiles = append([]string{}, readiness.Result.InspectedFiles...)
		return job.ID, &readiness
	}
	return "", nil
}

// Readiness is active business evidence until fulfillment owns a durable copy,
// not an ordinary completed-job deduplication receipt.
func commissionReadinessCount(state journalState, completedOnly bool) int {
	count := 0
	for _, job := range state.Jobs {
		if job.CommissionReadiness != nil && (!completedOnly || completedStatus(job.Status)) {
			count++
		}
	}
	return count
}

func clearCommissionReadiness(state *journalState, orderID string) bool {
	changed := false
	for i := range state.Jobs {
		if value := state.Jobs[i].CommissionReadiness; value != nil && value.Result.OrderID == orderID {
			state.Jobs[i].CommissionReadiness = nil
			changed = true
		}
	}
	return changed
}

// RetireCommissionReadiness releases protection only after the caller observes
// an authoritative terminal order. Historical CommissionResult stays intact.
func (j *Journal) RetireCommissionReadiness(orderID string) error {
	if _, err := positiveNotificationInteger([]byte(orderID)); err != nil {
		return errors.New("invalid_commission_readiness_order")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	next := cloneJournal(j.state)
	if !clearCommissionReadiness(&next, orderID) {
		return nil
	}
	return j.save(next)
}

// SaveCommissionReadiness persists the real inspected decision before accepting
// an order. It leaves the execution running until the acceptance outcome is known.
func (j *Journal) SaveCommissionReadiness(id string, value CommissionReadiness) error {
	if value.Result.RequestID != id || !validCommissionReadiness(value) {
		return errors.New("invalid_commission_readiness")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	next := cloneJournal(j.state)
	for i := range next.Jobs {
		job := &next.Jobs[i]
		if job.ID != id {
			continue
		}
		key, err := parseCommissionIntakeData(job.Data, j.binding.AgentID)
		if job.Kind != "commission_order" || job.Status != "running" || err != nil || key.RecipientRole != "seller" || !commissionResultMatchesNotification(value.Result, key) || job.CommissionReadiness != nil {
			return errors.New("commission_readiness_requires_running_inspection")
		}
		// Reject before the caller sends acceptance. Never evict an earlier
		// accepted order's only evidence to make room for another sale.
		if commissionReadinessCount(next, false) >= journalMaxPending {
			return ErrCommissionReadinessCapacity
		}
		job.CommissionReadiness = &value
		return j.save(next)
	}
	return errors.New("job_not_found")
}

// QueueCommissionFromReadiness binds an earlier real inspection to the current
// paid version without changing the historical decision's version or request ID.
func (j *Journal) QueueCommissionFromReadiness(id, sourceID, orderID string, orderVersion int64, execution ...CommissionFulfillmentExecution) error {
	if len(execution) > 1 {
		return errors.New("invalid_commission_fulfillment_execution")
	}
	payload := CommissionFulfillmentJob{Version: 1, OrderID: orderID, OrderVersion: orderVersion, IntakeRequestID: sourceID}
	if len(execution) == 1 {
		payload.ExecutionRevision, payload.DeliveryReview = execution[0].Revision, execution[0].Review
	}
	if payload.ExecutionRevision < 0 || payload.DeliveryReview != nil && (!payload.DeliveryReview.AllowsRework() || payload.ExecutionRevision <= 0) {
		return errors.New("invalid_commission_fulfillment_rework")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	next := cloneJournal(j.state)
	var readiness *CommissionReadiness
	for _, job := range next.Jobs {
		if job.ID == sourceID && job.Kind == "commission_order" {
			readiness = job.CommissionReadiness
			break
		}
		if job.ID == sourceID && job.Kind == "commission_fulfillment" && payload.DeliveryReview != nil {
			var previous CommissionFulfillmentJob
			if json.Unmarshal(job.Data, &previous) == nil && previous.OrderID == orderID && previous.OrderVersion < orderVersion && previous.DeliveryReview == nil {
				readiness = &CommissionReadiness{Result: previous.IntakeResult, InputFingerprint: previous.InputFingerprint}
				payload.IntakeRequestID = previous.IntakeRequestID
			}
			break
		}
	}
	if readiness == nil || !validCommissionReadiness(*readiness) || readiness.Result.OrderID != orderID || readiness.Result.OrderVersion >= orderVersion {
		return errors.New("commission_readiness_unavailable")
	}
	for i := range next.Jobs {
		job := &next.Jobs[i]
		if job.ID != id {
			continue
		}
		key, err := parseCommissionIntakeData(job.Data, j.binding.AgentID)
		notificationVersion, versionErr := strconv.ParseInt(key.OrderVersion, 10, 64)
		if job.Kind != "commission_order" || job.Status != "running" || err != nil || versionErr != nil || notificationVersion > orderVersion || key.RecipientRole != "seller" || key.OrderID != orderID {
			return errors.New("commission_fulfillment_requires_paid_seller")
		}
		payload.IntakeResult, payload.InputFingerprint = readiness.Result, readiness.InputFingerprint
		fulfillmentID := commissionFulfillmentID(j.binding, orderID, payload)
		found := false
		for _, existing := range next.Jobs {
			if existing.ID == fulfillmentID {
				found = true
				break
			}
			if payload.DeliveryReview != nil && existing.Kind == "commission_fulfillment" {
				var previous CommissionFulfillmentJob
				if json.Unmarshal(existing.Data, &previous) == nil && previous.OrderID == orderID && previous.OrderVersion == orderVersion && previous.DeliveryReview != nil {
					found = true
					break
				}
			}
		}
		if !found {
			data, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			next.Jobs = append(next.Jobs, j.newHint(fulfillmentID, "commission_fulfillment", data))
		}
		// The fulfillment payload now owns the exact evidence. Release all
		// source protection atomically with its durable queue admission.
		clearCommissionReadiness(&next, orderID)
		job = &next.Jobs[i]
		if job.CommissionDiagnostic == nil {
			job.CommissionDiagnostic = &CommissionDiagnostic{}
		}
		job.CommissionDiagnostic.CheckReceipt = CheckReceipt(readiness.Result, readiness.InputFingerprint)
		job.Status, job.Code = "completed", "commission_intake_ready_reused"
		return j.saveUpdatedJob(next, i)
	}
	return errors.New("job_not_found")
}
