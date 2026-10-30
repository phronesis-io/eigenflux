package dispatch

import "encoding/json"

// HasRecordedCommissionOrder prevents automatic migration from replaying any
// known business outcome, including failed, unknown and completed work.
func (j *Journal) HasRecordedCommissionOrder(orderID string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.hasRecordedCommissionOrderLocked(orderID)
}

func (j *Journal) hasRecordedCommissionOrderLocked(orderID string) bool {
	for _, job := range j.state.Jobs {
		if job.CommissionResult != nil && job.CommissionResult.OrderID == orderID {
			return true
		}
		if job.CommissionReadiness != nil && job.CommissionReadiness.Result.OrderID == orderID {
			return true
		}
		if job.CommissionExecution != nil && job.CommissionExecution.OrderID == orderID {
			return true
		}
		if completedStatus(job.Status) && len(job.Data) == 0 {
			continue
		}
		switch job.Kind {
		case "commission_order":
			key, err := parseCommissionIntakeData(job.Data, j.binding.AgentID)
			if err != nil || key.OrderID == orderID {
				return true
			}
		case "commission_fulfillment", "commission_clarification":
			var data struct {
				OrderID string `json:"order_id"`
			}
			if json.Unmarshal(job.Data, &data) != nil || data.OrderID == "" || data.OrderID == orderID {
				return true
			}
		}
	}
	return false
}

// RecoverUnrecordedCommission atomically skips every previously recorded outcome
// before creating migration work. It never turns migration into an operator retry.
func (j *Journal) RecoverUnrecordedCommission(orderID string, version int64) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.hasRecordedCommissionOrderLocked(orderID) {
		return false, nil
	}
	_, err := j.recoverCommissionLocked(orderID, version, "notification_migration", "notification_migration")
	return err == nil, err
}
