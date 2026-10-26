package dispatch

import (
	"encoding/json"
	"errors"
	"time"
)

const CommissionDeliveryMaxAttempts = 5

// CommissionDeliveryRetry admits only replay of verified bytes under the original
// upload/delivery keys. It never admits another Agent execution.
type CommissionDeliveryRetry struct {
	Attempts      int    `json:"attempts"`
	NextAttemptAt int64  `json:"next_attempt_at,omitempty"`
	LastCode      string `json:"last_code,omitempty"`
	BuyerID       string `json:"buyer_id"`
	SellerID      string `json:"seller_id"`
	SnapshotID    string `json:"snapshot_id"`
}

func commissionDeliveryDelay(attempts int) time.Duration {
	return []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}[attempts-1]
}

func validCommissionDelivery(job Job) bool {
	r := job.CommissionDelivery
	if r == nil {
		return true
	}
	if job.Kind != "commission_fulfillment" || r.Attempts < 1 || r.Attempts > CommissionDeliveryMaxAttempts || r.NextAttemptAt < 0 {
		return false
	}
	for _, id := range []string{r.BuyerID, r.SellerID, r.SnapshotID} {
		if _, err := positiveNotificationInteger(json.RawMessage(id)); err != nil {
			return false
		}
	}
	if r.BuyerID == r.SellerID || (job.CommissionCheckpoint == nil && job.CommissionFulfillment == nil) {
		return false
	}
	if r.NextAttemptAt > 0 {
		return job.Status == "pending" && job.Code == "commission_delivery_retry_pending" && r.Attempts < CommissionDeliveryMaxAttempts
	}
	return job.Status != "pending"
}

func (j *Journal) InitializeCommissionDelivery(id, buyerID, sellerID, snapshotID string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	next := cloneJournal(j.state)
	for i := range next.Jobs {
		job := &next.Jobs[i]
		if job.ID != id {
			continue
		}
		if job.CommissionDelivery != nil {
			r := job.CommissionDelivery
			if r.BuyerID != buyerID || r.SellerID != sellerID || r.SnapshotID != snapshotID {
				return errors.New("commission_delivery_identity_changed")
			}
			return nil
		}
		if job.Kind != "commission_fulfillment" || job.Status != "running" || job.CommissionCheckpoint == nil || job.CommissionCheckpoint.Decision.Outcome != "artifacts_ready" {
			return errors.New("commission_delivery_requires_checkpoint")
		}
		job.CommissionDelivery = &CommissionDeliveryRetry{Attempts: 1, BuyerID: buyerID, SellerID: sellerID, SnapshotID: snapshotID}
		if !validCommissionDelivery(*job) {
			return errors.New("invalid_commission_delivery_retry")
		}
		return j.save(next)
	}
	return errors.New("job_not_found")
}

// ScheduleCommissionDeliveryRetry persists the same job and evidence. The next
// claim consumes an attempt; waiting never occupies the serial execution lane.
func (j *Journal) ScheduleCommissionDeliveryRetry(id, code, session string, now time.Time) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	next := cloneJournal(j.state)
	for i := range next.Jobs {
		job := &next.Jobs[i]
		if job.ID != id {
			continue
		}
		if job.CommissionDelivery == nil {
			return false, nil
		}
		if (job.Status != "running" && job.Status != "sending") || !validCommissionDelivery(*job) {
			return false, errors.New("commission_delivery_not_schedulable")
		}
		if len(code) == 0 || len(code) > 128 {
			return false, errors.New("invalid_commission_delivery_error_code")
		}
		job.CommissionDelivery.LastCode = code
		if session != "" {
			job.SessionID = session
		}
		if job.CommissionDelivery.Attempts >= CommissionDeliveryMaxAttempts {
			job.Status = "failed"
			if job.CommissionFulfillment != nil {
				job.Status = "unknown"
			}
			job.Code = "commission_delivery_retry_exhausted"
		} else {
			job.CommissionDelivery.NextAttemptAt = now.Add(commissionDeliveryDelay(job.CommissionDelivery.Attempts)).UnixMilli()
			job.Status, job.Code = "pending", "commission_delivery_retry_pending"
		}
		if err := j.save(next); err != nil {
			return false, err
		}
		j.signal()
		return true, nil
	}
	return false, errors.New("job_not_found")
}

func recoverCommissionDelivery(job *Job, now time.Time) bool {
	if job.CommissionDelivery == nil || (job.Status != "running" && job.Status != "sending") {
		return false
	}
	if job.CommissionDelivery.Attempts >= CommissionDeliveryMaxAttempts {
		job.Status, job.Code = "unknown", "commission_delivery_retry_exhausted"
		if job.CommissionFulfillment == nil {
			job.Status = "failed"
		}
	} else {
		job.Status, job.Code = "pending", "commission_delivery_retry_pending"
		job.CommissionDelivery.NextAttemptAt = now.Add(commissionDeliveryDelay(job.CommissionDelivery.Attempts)).UnixMilli()
	}
	return true
}
