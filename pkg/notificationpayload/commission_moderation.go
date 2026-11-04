package notificationpayload

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

const SourceCommissionModeration = "commission_moderation"

const CommissionModerationTopic = "commission.moderation.changed.v1"

type CommissionModeration struct {
	NotificationVersion int64  `json:"notification_version"`
	ModerationID        string `json:"moderation_id"`
	CommissionID        string `json:"commission_id"`
	RecipientAgentID    string `json:"recipient_agent_id"`
	DraftVersion        int64  `json:"draft_version"`
	Status              string `json:"status"`
	Decision            string `json:"decision"`
	Flag                string `json:"flag"`
	Field               string `json:"field"`
	Reason              string `json:"reason"`
	OccurredAt          int64  `json:"occurred_at"`
}

func ParseCommissionModeration(raw string) (CommissionModeration, error) {
	var payload CommissionModeration
	invalid := errors.New("invalid commission moderation notification payload")
	if len(raw) > 64<<10 || json.Unmarshal([]byte(raw), &payload) != nil {
		return payload, invalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil {
		return payload, invalid
	}
	for _, name := range []string{"decision", "flag", "field", "reason"} {
		value, exists := fields[name]
		if !exists || string(value) == "null" {
			return payload, invalid
		}
	}
	for _, id := range []string{payload.ModerationID, payload.CommissionID, payload.RecipientAgentID} {
		value, err := strconv.ParseInt(id, 10, 64)
		if err != nil || value <= 0 || strconv.FormatInt(value, 10) != id {
			return payload, invalid
		}
	}
	if payload.NotificationVersion <= 0 || payload.DraftVersion <= 0 || payload.OccurredAt <= 0 {
		return payload, invalid
	}
	switch payload.Status {
	case "pending":
		if payload.Decision != "" {
			return payload, invalid
		}
	case "pass", "published":
		if payload.Decision != "pass" {
			return payload, invalid
		}
	case "review", "reject":
		if payload.Decision != payload.Status || strings.TrimSpace(payload.Reason) == "" {
			return payload, invalid
		}
	case "failed", "stale":
		if payload.Decision != "" && payload.Decision != "pass" && payload.Decision != "review" && payload.Decision != "reject" {
			return payload, invalid
		}
	default:
		return payload, invalid
	}
	return payload, nil
}

// ModerationForAgent validates the payload and its authenticated recipient.
func ModerationForAgent(raw string, agentID int64) (CommissionModeration, error) {
	payload, err := ParseCommissionModeration(raw)
	if err != nil {
		return CommissionModeration{}, err
	}
	if agentID <= 0 || payload.RecipientAgentID != strconv.FormatInt(agentID, 10) {
		return CommissionModeration{}, errors.New("moderation notification recipient mismatch")
	}
	return payload, nil
}

func (p CommissionModeration) Content() string {
	content := "Service listing moderation: " + p.Status
	if p.Reason != "" {
		content += " — " + p.Reason
	}
	return content
}
