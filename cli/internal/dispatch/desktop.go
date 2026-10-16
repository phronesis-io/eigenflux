package dispatch

import "encoding/json"

// CommissionBlocker contains routing metadata only; it never exposes order inputs.
type CommissionBlocker struct {
	ID, OrderID, Role, Status, Code string
}

// PrivateMessageBlocker exposes no message body or counterparty-supplied routing.
type PrivateMessageBlocker struct {
	ID, Code string
}

func (j *Journal) PrivateMessageBlockers() []PrivateMessageBlocker {
	j.mu.Lock()
	defer j.mu.Unlock()
	result := []PrivateMessageBlocker{}
	for _, job := range j.state.Jobs {
		if job.Kind == "pm_push" && job.Status == "needs_user" {
			result = append(result, PrivateMessageBlocker{ID: job.ID, Code: job.Code})
		}
	}
	return result
}

// CommissionBlockers recovers unresolved work for user notification without
// weakening Snapshot's redaction or changing execution/remote ACK state.
func (j *Journal) CommissionBlockers() ([]CommissionBlocker, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	result := []CommissionBlocker{}
	for _, job := range j.state.Jobs {
		if (job.Kind != "commission_order" && job.Kind != "commission_fulfillment") || (job.Status != "needs_user" && job.Status != "failed" && job.Status != "unknown") {
			continue
		}
		item := CommissionBlocker{ID: job.ID, Status: job.Status, Code: job.Code, Role: "seller"}
		if job.Kind == "commission_order" {
			key, err := parseCommissionIntakeData(job.Data, j.binding.AgentID)
			if err != nil {
				return nil, err
			}
			item.OrderID, item.Role = key.OrderID, key.RecipientRole
		} else {
			var data CommissionFulfillmentJob
			if err := json.Unmarshal(job.Data, &data); err != nil {
				return nil, err
			}
			item.OrderID = data.OrderID
		}
		result = append(result, item)
	}
	return result, nil
}
