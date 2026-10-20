package dispatch

import (
	"encoding/json"
	"strconv"
)

// CommissionBlocker contains routing metadata only; it never exposes order inputs.
type CommissionBlocker struct {
	ID, OrderID, Role, Status, Code string
	Kind                            string
	OrderVersion                    int64
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
		item := CommissionBlocker{ID: job.ID, Kind: job.Kind, Status: job.Status, Code: job.Code, Role: "seller"}
		if job.Kind == "commission_order" {
			key, err := parseCommissionIntakeData(job.Data, j.binding.AgentID)
			if err != nil {
				return nil, err
			}
			item.OrderID, item.Role = key.OrderID, key.RecipientRole
			item.OrderVersion, _ = strconv.ParseInt(key.OrderVersion, 10, 64)
		} else {
			var data CommissionFulfillmentJob
			if err := json.Unmarshal(job.Data, &data); err != nil {
				return nil, err
			}
			item.OrderID = data.OrderID
			item.OrderVersion = data.OrderVersion
		}
		if execution := job.CommissionExecution; execution != nil && execution.OrderID == item.OrderID && execution.OrderVersion > 0 {
			item.OrderVersion = execution.OrderVersion
		}
		result = append(result, item)
	}
	return result, nil
}
