package dispatch

import (
	"encoding/json"
	"errors"
	"strconv"
)

// RecoverCommission queues authoritative order inspection without inventing a
// server notification or acknowledging the notification inbox.
func (j *Journal) RecoverCommission(orderID string, version int64) (string, error) {
	if !j.binding.Handles("commission_order") {
		return "", errors.New("commission_event_not_subscribed")
	}
	if n, err := strconv.ParseInt(orderID, 10, 64); err != nil || n <= 0 || version <= 0 {
		return "", errors.New("invalid_commission_order")
	}
	id := commissionJournalID(j.binding, commissionNotificationKey{OrderID: orderID, OrderVersion: strconv.FormatInt(version, 10), RecipientRole: "seller"})
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, job := range j.state.Jobs {
		if job.Kind == "commission_order" && (job.Status == "unknown" || job.Status == "running" || job.Status == "sending") {
			existing, err := parseCommissionIntakeData(job.Data, j.binding.AgentID)
			if err != nil || existing.OrderID == orderID {
				return "", errors.New("commission_intake_unresolved_use_job_recovery")
			}
		}
		if job.ID == commissionFulfillmentID(j.binding, orderID) {
			return "", errors.New("commission_fulfillment_exists_use_job_recovery")
		}
		if job.ID == id {
			return id, nil
		}
	}
	raw, _ := json.Marshal(map[string]any{"source": "operator_recovery", "payload": map[string]any{"order_id": orderID, "order_version": version, "recipient_role": "seller"}})
	next := cloneJournal(j.state)
	job := j.newHint(id, "commission_order", raw)
	job.Code = "operator_retry"
	next.Jobs = append(next.Jobs, job)
	if err := j.save(next); err != nil {
		return "", err
	}
	j.signal()
	return id, nil
}

func parseCommissionIntakeData(raw json.RawMessage, agentID string) (commissionNotificationKey, error) {
	var recovery struct {
		Source  string `json:"source"`
		Payload struct {
			OrderID string `json:"order_id"`
			Version int64  `json:"order_version"`
			Role    string `json:"recipient_role"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &recovery) == nil && recovery.Source == "operator_recovery" {
		if n, err := strconv.ParseInt(recovery.Payload.OrderID, 10, 64); err != nil || n <= 0 || recovery.Payload.Version <= 0 || recovery.Payload.Role != "seller" {
			return commissionNotificationKey{}, errors.New("invalid_commission_recovery")
		}
		return commissionNotificationKey{OrderID: recovery.Payload.OrderID, OrderVersion: strconv.FormatInt(recovery.Payload.Version, 10), RecipientRole: "seller"}, nil
	}
	return parseCommissionNotification(raw, agentID)
}
