package dispatch

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CommissionClarificationJob stores only the authoritative request fence.
type CommissionClarificationJob struct {
	Version      int    `json:"version"`
	OrderID      string `json:"order_id"`
	OrderVersion int64  `json:"order_version"`
	BuyerID      string `json:"buyer_agent_id"`
	RequestID    string `json:"input_request_id"`
	Round        int    `json:"round"`
}

type CommissionClarificationDecision struct {
	Version        int    `json:"version"`
	RequestID      string `json:"request_id"`
	OrderID        string `json:"order_id"`
	OrderVersion   int64  `json:"order_version"`
	InputRequestID string `json:"input_request_id"`
	Outcome        string `json:"outcome"`
	Answer         string `json:"answer"`
	Summary        string `json:"summary"`
}

func validCommissionClarification(data CommissionClarificationJob, buyerID string) bool {
	_, err := positiveNotificationInteger(json.RawMessage(`"` + data.OrderID + `"`))
	return err == nil && data.Version == 1 && data.OrderVersion > 0 && data.BuyerID == buyerID && executionReceiptPattern.MatchString(data.RequestID) && data.Round >= 1 && data.Round <= 3
}

func ParseCommissionClarificationDecision(raw, requestID string, job CommissionClarificationJob) (CommissionClarificationDecision, error) {
	var result CommissionClarificationDecision
	if len(raw) > 32<<10 || !utf8.ValidString(raw) {
		return result, errors.New("invalid_clarification_result")
	}
	if err := requiredCommissionFields([]byte(raw), "version", "request_id", "order_id", "order_version", "input_request_id", "outcome", "answer", "summary"); err != nil {
		return result, err
	}
	if err := decodeStrictObject([]byte(raw), &result); err != nil {
		return result, err
	}
	if result.Version != 1 || result.RequestID != requestID || result.OrderID != job.OrderID || result.OrderVersion != job.OrderVersion || result.InputRequestID != job.RequestID || !utf8.ValidString(result.Answer) || !utf8.ValidString(result.Summary) || strings.TrimSpace(result.Summary) == "" || len(result.Summary) > 2000 || len(result.Answer) > 8000 {
		return result, errors.New("clarification_result_identity_or_size_mismatch")
	}
	for _, text := range []string{result.Answer, result.Summary} {
		if strings.IndexFunc(text, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' }) >= 0 {
			return result, errors.New("invalid_clarification_control_character")
		}
	}
	if result.Outcome == "answered" && strings.TrimSpace(result.Answer) != "" || result.Outcome == "waiting" && result.Answer == "" {
		return result, nil
	}
	return result, errors.New("invalid_clarification_outcome")
}

func commissionClarificationID(b Binding, orderID, requestID string) string {
	return journalID(b.Scope, b.Revision, "commission_clarification", orderID, requestID)
}

// QueueCommissionClarification atomically completes buyer notice intake and
// admits at most one answer attempt for the server's request ID.
func (j *Journal) QueueCommissionClarification(sourceID string, data CommissionClarificationJob) error {
	if !validCommissionClarification(data, j.binding.AgentID) {
		return errors.New("invalid_clarification_job")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	next := cloneJournal(j.state)
	for i := range next.Jobs {
		source := &next.Jobs[i]
		if source.ID != sourceID {
			continue
		}
		key, err := parseCommissionIntakeData(source.Data, j.binding.AgentID)
		notificationVersion, versionErr := strconv.ParseInt(key.OrderVersion, 10, 64)
		if source.Kind != "commission_order" || source.Status != "running" || err != nil || versionErr != nil || notificationVersion > data.OrderVersion || key.RecipientRole != "buyer" || key.OrderID != data.OrderID {
			return errors.New("clarification_requires_buyer_notice")
		}
		id := commissionClarificationID(j.binding, data.OrderID, data.RequestID)
		found := false
		for _, job := range next.Jobs {
			if job.ID == id {
				found = true
				break
			}
		}
		source.Status, source.Code = "completed", "commission_clarification_queued"
		if !found {
			raw, _ := json.Marshal(data)
			next.Jobs = append(next.Jobs, j.newHint(id, "commission_clarification", raw))
		}
		return j.save(next)
	}
	return errors.New("job_not_found")
}
