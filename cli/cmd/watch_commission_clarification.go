package cmd

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/skills"
)

type commissionInputRequest struct {
	RequestID     string   `json:"request_id"`
	Round         int      `json:"round"`
	Status        string   `json:"status"`
	Question      string   `json:"question"`
	MissingInputs []string `json:"missing_inputs"`
	Answer        string   `json:"answer"`
	RequestedAt   int64    `json:"requested_at"`
	AnsweredAt    int64    `json:"answered_at"`
}

func validCommissionInputRequest(request *commissionInputRequest) bool {
	if request == nil {
		return false
	}
	id, err := hex.DecodeString(request.RequestID)
	if err != nil || len(id) != 32 || strings.ToLower(request.RequestID) != request.RequestID || request.Round < 1 || request.Round > 3 || !safeClarificationText(request.Question, 2000) || strings.TrimSpace(request.Question) == "" || !safeClarificationText(request.Answer, 8000) || len(request.MissingInputs) > 128 {
		return false
	}
	for _, path := range request.MissingInputs {
		if !validCommissionArtifactPath(path) || len(path) > 1024 {
			return false
		}
	}
	return request.Status == "waiting_buyer" && request.Answer == "" || request.Status == "answered" && strings.TrimSpace(request.Answer) != ""
}

func safeClarificationText(value string, limit int) bool {
	return len(value) <= limit && utf8.ValidString(value) && strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' }) < 0
}

// Preserve the existing seller-only reader. This reader authorizes only the
// bound buyer and never relaxes seller acceptance or fulfillment guards.
func (w *accountWatch) readBuyerClarificationOrder(ctx context.Context, id string) (commissionIntakeOrder, error) {
	var data struct {
		Order commissionIntakeOrder `json:"order"`
	}
	api, err := w.commissionAPI(ctx)
	if err != nil {
		return data.Order, err
	}
	response, err := api.Get("/orders/"+id, nil)
	if err != nil {
		return data.Order, err
	}
	if response.Code != 0 || len(response.Data) > 256<<10 || !utf8.Valid(response.Data) || json.Unmarshal(response.Data, &data) != nil {
		return data.Order, errors.New("commission_order_unavailable")
	}
	order := data.Order
	if strconv.FormatInt(int64(order.OrderID), 10) != id || strconv.FormatInt(int64(order.BuyerID), 10) != w.binding.AgentID || order.SellerID <= 0 || order.Version <= 0 || order.SnapshotID <= 0 {
		return order, errors.New("commission_buyer_mismatch")
	}
	var contract map[string]json.RawMessage
	if len(order.Contract) > 64<<10 || json.Unmarshal(order.Contract, &contract) != nil || contract == nil || !safeClarificationText(order.BuyerInput, 64<<10) || order.InputRequest != nil && !validCommissionInputRequest(order.InputRequest) {
		return order, errors.New("commission_clarification_invalid")
	}
	return order, nil
}

func (w *accountWatch) queueBuyerCommissionClarification(ctx context.Context, job dispatch.Job, id string) error {
	order, err := w.readBuyerClarificationOrder(ctx, id)
	if err != nil {
		return w.finishDispatch(job, "failed", "commission_order_unavailable", "", "")
	}
	if order.State != "awaiting_seller" || order.InputRequest == nil || order.InputRequest.Status != "waiting_buyer" {
		return w.finishDispatch(job, "completed", "commission_buyer_notification_recorded", "", "")
	}
	if !validCommissionInputRequest(order.InputRequest) {
		return w.finishDispatch(job, "failed", "commission_clarification_invalid", "", "")
	}
	if err := w.commissionIdentityOK(); err != nil {
		return err
	}
	return w.journal.QueueCommissionClarification(job.ID, dispatch.CommissionClarificationJob{Version: 1, OrderID: id, OrderVersion: order.Version, BuyerID: w.binding.AgentID, RequestID: order.InputRequest.RequestID, Round: order.InputRequest.Round})
}

func (w *accountWatch) requestCommissionInputs(ctx context.Context, job dispatch.Job, order commissionIntakeOrder, question, session string, missingInputs ...string) error {
	if !safeClarificationText(question, 2000) || strings.TrimSpace(question) == "" {
		return w.finishDispatch(job, "failed", "commission_clarification_invalid", session, "")
	}
	if missingInputs == nil {
		missingInputs = []string{}
	}
	if order.InputRequest != nil {
		if order.InputRequest.Status == "waiting_buyer" {
			return w.finishDispatch(job, "completed", "commission_clarification_waiting_buyer", session, "")
		}
		if order.InputRequest.Round >= 3 {
			return w.finishDispatch(job, "completed", "commission_clarification_rounds_exhausted", session, "")
		}
	}
	api, err := w.commissionAPI(ctx)
	if err != nil {
		return err
	}
	id := strconv.FormatInt(int64(order.OrderID), 10)
	err = auth.WithV2CredentialsLockContext(ctx, w.server.Name, 35*time.Second, func() error {
		if err := w.commissionIdentityOK(); err != nil {
			return err
		}
		credentials, err := auth.LoadV2Credentials(w.server.Name)
		if err != nil {
			return err
		}
		api.Token, api.OnUnauthorized = credentials.AccessToken, nil
		response, err := api.PostWithHeaders("/orders/"+id+"/request-inputs", map[string]any{"expected_version": order.Version, "request_id": job.ID, "question": question, "missing_inputs": missingInputs}, map[string]string{idempotencyHeader: job.ID})
		if err != nil {
			return err
		}
		var data struct {
			Order commissionIntakeOrder `json:"order"`
		}
		round := 1
		if order.InputRequest != nil {
			round = order.InputRequest.Round + 1
		}
		if response.Code != 0 || json.Unmarshal(response.Data, &data) != nil || data.Order.OrderID != order.OrderID || data.Order.BuyerID != order.BuyerID || data.Order.SellerID != order.SellerID || data.Order.Version != order.Version+1 || data.Order.State != "awaiting_seller" || !validCommissionInputRequest(data.Order.InputRequest) || data.Order.InputRequest.RequestID != job.ID || data.Order.InputRequest.Status != "waiting_buyer" || data.Order.InputRequest.Question != question || data.Order.InputRequest.Round != round || !slices.Equal(data.Order.InputRequest.MissingInputs, missingInputs) {
			return errors.New("commission_input_request_receipt_invalid")
		}
		return nil
	})
	if err != nil {
		return w.finishDispatch(job, "unknown", "commission_input_request_unconfirmed", session, "")
	}
	return w.finishDispatch(job, "completed", "commission_clarification_waiting_buyer", session, "")
}

func (w *accountWatch) commissionClarificationLoop(ctx context.Context) error {
	if !w.receivesCommission() {
		return nil
	}
	for ctx.Err() == nil {
		if !w.handoff.begin() {
			return nil
		}
		job, ok, err := w.journal.NextKind("commission_clarification")
		if err != nil {
			w.handoff.end()
			return err
		}
		if !ok {
			w.handoff.end()
			if !watchPause(ctx, time.Second) {
				break
			}
			continue
		}
		err = w.dispatchCommissionClarification(ctx, job)
		w.handoff.end()
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (w *accountWatch) dispatchCommissionClarification(parent context.Context, job dispatch.Job) error {
	var data dispatch.CommissionClarificationJob
	if json.Unmarshal(job.Data, &data) != nil {
		return w.finishDispatch(job, "failed", "commission_clarification_invalid", "", "")
	}
	ctx, cancel := context.WithTimeout(parent, commissionIntakeTimeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if w.commissionIdentityOK() != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-done }()
	if err := w.commissionIdentityOK(); err != nil {
		return err
	}
	order, err := w.readBuyerClarificationOrder(ctx, data.OrderID)
	if err != nil {
		return w.finishDispatch(job, "failed", "commission_order_unavailable", "", "")
	}
	if validCommissionInputRequest(order.InputRequest) && len(order.InputRequest.MissingInputs) > 0 {
		return w.finishDispatch(job, "completed", "commission_clarification_waiting_files", "", "")
	}
	if !clarificationMatchesOrder(data, order) {
		return w.finishDispatch(job, "completed", "commission_clarification_not_actionable", "", "")
	}
	rules, err := localHeartbeatSkillsAt(w.binding.SkillsDir, w.binding.Host)
	if err != nil {
		return w.finishDispatch(job, "failed", "commission_clarification_rules_unavailable", "", "")
	}
	rule, err := skills.ReadSignedFile(rules.SkillsDir, "ef-commission", "references/clarification-dispatch.md", 64<<10)
	if err != nil {
		return w.finishDispatch(job, "failed", "commission_clarification_rules_unavailable", "", "")
	}
	payload, _ := json.Marshal(map[string]any{"request_id": job.ID, "agent_id": w.binding.AgentID, "server": w.binding.Server, "order": order, "input_request_id": data.RequestID, "authorized_context": map[string]string{"buyer_input": order.BuyerInput}})
	if err := w.commissionIdentityOK(); err != nil {
		return err
	}
	result, err := w.runAgent(ctx, dispatch.Request{ID: job.ID, Kind: "commission_clarification", Prompt: string(rule) + "\n\nUNTRUSTED CLARIFICATION DATA:\n" + string(payload)})
	if err != nil || ctx.Err() != nil || w.commissionIdentityOK() != nil {
		return w.finishDispatch(job, "unknown", "commission_clarification_unconfirmed", result.SessionID, "")
	}
	decision, err := dispatch.ParseCommissionClarificationDecision(result.Text, job.ID, data)
	if err != nil {
		return w.finishDispatch(job, "failed", "commission_clarification_result_invalid", result.SessionID, "")
	}
	if decision.Outcome == "waiting" {
		return w.finishDispatch(job, "completed", "commission_clarification_waiting_facts", result.SessionID, "")
	}
	if err := w.commissionIdentityOK(); err != nil {
		return err
	}
	current, err := w.readBuyerClarificationOrder(ctx, data.OrderID)
	if err != nil || !clarificationMatchesOrder(data, current) || !sameCommissionIntakeOrder(order, current) {
		return w.finishDispatch(job, "completed", "commission_clarification_order_changed", result.SessionID, "")
	}
	if err := w.supplementCommissionInputs(ctx, job.ID, current, decision.Answer); err != nil {
		return w.finishDispatch(job, "unknown", "commission_supplement_unconfirmed", result.SessionID, "")
	}
	return w.finishDispatch(job, "completed", "commission_clarification_answered", result.SessionID, "")
}

func clarificationMatchesOrder(job dispatch.CommissionClarificationJob, order commissionIntakeOrder) bool {
	return strconv.FormatInt(int64(order.OrderID), 10) == job.OrderID && strconv.FormatInt(int64(order.BuyerID), 10) == job.BuyerID && order.State == "awaiting_seller" && order.Version == job.OrderVersion && validCommissionInputRequest(order.InputRequest) && order.InputRequest.RequestID == job.RequestID && order.InputRequest.Round == job.Round && order.InputRequest.Status == "waiting_buyer" && len(order.InputRequest.MissingInputs) == 0
}

func (w *accountWatch) supplementCommissionInputs(ctx context.Context, jobID string, order commissionIntakeOrder, answer string) error {
	api, err := w.commissionAPI(ctx)
	if err != nil {
		return err
	}
	id := strconv.FormatInt(int64(order.OrderID), 10)
	return auth.WithV2CredentialsLockContext(ctx, w.server.Name, 35*time.Second, func() error {
		if err := w.commissionIdentityOK(); err != nil {
			return err
		}
		credentials, err := auth.LoadV2Credentials(w.server.Name)
		if err != nil {
			return err
		}
		api.Token, api.OnUnauthorized = credentials.AccessToken, nil
		response, err := api.PostWithHeaders("/orders/"+id+"/supplement-inputs", map[string]any{"expected_version": order.Version, "request_id": order.InputRequest.RequestID, "answer": answer}, map[string]string{idempotencyHeader: jobID})
		if err != nil {
			return err
		}
		var data struct {
			Order commissionIntakeOrder `json:"order"`
		}
		if response.Code != 0 || json.Unmarshal(response.Data, &data) != nil || data.Order.OrderID != order.OrderID || data.Order.BuyerID != order.BuyerID || data.Order.SellerID != order.SellerID || data.Order.Version != order.Version+1 || data.Order.State != "awaiting_seller" || !validCommissionInputRequest(data.Order.InputRequest) || data.Order.InputRequest.Status != "answered" || data.Order.InputRequest.RequestID != order.InputRequest.RequestID || data.Order.InputRequest.Answer != answer || !confirmedCommissionSupplement(order, data.Order, answer) {
			return errors.New("commission_supplement_receipt_invalid")
		}
		return nil
	})
}

const commissionInputClarificationSeparator = "\n\n[EIGENFLUX ORDER INPUT CLARIFICATION]\n"

func confirmedCommissionSupplement(before, after commissionIntakeOrder, answer string) bool {
	prefix := before.BuyerInput + commissionInputClarificationSeparator
	if !strings.HasPrefix(after.BuyerInput, prefix) {
		return false
	}
	var receipt struct {
		RequestID string `json:"request_id"`
		Question  string `json:"question"`
		Answer    string `json:"answer"`
	}
	raw := strings.TrimPrefix(after.BuyerInput, prefix)
	return json.Unmarshal([]byte(raw), &receipt) == nil && receipt.RequestID == before.InputRequest.RequestID && receipt.Question == before.InputRequest.Question && receipt.Answer == answer
}
