package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/dispatch"
)

func clarificationBuyerOrder() commissionIntakeOrder {
	order := testCommissionOrder()
	order.BuyerID, order.SellerID, order.Version, order.State = 42, 41, 4, "awaiting_seller"
	order.BuyerInput = "Write the report for the finance team."
	order.InputRequest = &commissionInputRequest{RequestID: strings.Repeat("a", 64), Round: 1, Status: "waiting_buyer", Question: "Who is the report audience?", MissingInputs: []string{}}
	return order
}

func claimBuyerClarification(t *testing.T, w *accountWatch, version int64) dispatch.Job {
	t.Helper()
	raw := strings.Replace(string(commissionTestNotification(81, 901, "42")), `"recipient_role":"seller"`, `"recipient_role":"buyer"`, 1)
	raw = strings.Replace(raw, `"order_version":3`, fmt.Sprintf(`"order_version":%d`, version), 1)
	raw = strings.Replace(raw, "order.state.changed.v1", "order.inputs.requested.v1", 1)
	if err := w.journal.AddCommissionNotification(json.RawMessage(raw)); err != nil {
		t.Fatal(err)
	}
	source, ok, err := w.journal.NextKind("commission_order")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := w.dispatchCommissionIntake(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	job, ok, err := w.journal.NextKind("commission_clarification")
	if err != nil || !ok {
		t.Fatalf("clarification claim: %v %v jobs=%+v", ok, err, w.journal.Snapshot())
	}
	return job
}

func TestCommissionClarificationBuyerAnswersOnlyKnownInputs(t *testing.T) {
	for _, scenario := range []string{"answered", "multiline_answer", "waiting", "missing_files", "wrong_buyer", "run_error", "invalid_result", "changed_order", "changed_identity", "cancelled", "uncertain_write", "invalid_receipt"} {
		t.Run(scenario, func(t *testing.T) {
			order := clarificationBuyerOrder()
			answer := "The finance team."
			if scenario == "multiline_answer" {
				answer = "面向\"财务团队\"。\n保留路径 C:\\Reports。"
			}
			if scenario == "missing_files" {
				order.InputRequest.MissingInputs = []string{"requirements.pdf"}
			}
			writes, ran := 0, false
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/orders/901" && r.Method == http.MethodGet {
					current := order
					if scenario == "wrong_buyer" && ran {
						current.BuyerID = 43
					}
					if scenario == "changed_order" && ran {
						current.Version++
					}
					materialTestResponse(out, map[string]any{"order": current})
					return
				}
				if r.URL.Path == "/api/v2/console/trade/orders/901" && r.Method == http.MethodGet {
					materialTestResponse(out, buyerClarificationTestDetail(order, []map[string]any{}))
					return
				}
				if r.URL.Path == "/api/v1/orders/901/supplement-inputs" && r.Method == http.MethodPost {
					writes++
					if !ran || scenario != "answered" && scenario != "multiline_answer" && scenario != "uncertain_write" && scenario != "invalid_receipt" {
						t.Error("unexpected write")
					}
					var body struct {
						Version   int64  `json:"expected_version"`
						RequestID string `json:"request_id"`
						Answer    string `json:"answer"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body.Version != 4 || body.RequestID != order.InputRequest.RequestID || body.Answer != answer || len(r.Header.Get(idempotencyHeader)) != 64 || r.Header.Get("Authorization") != "Bearer test-v2" {
						t.Error("unpinned supplement")
					}
					if scenario == "uncertain_write" {
						http.Error(out, "unknown", 503)
						return
					}
					updated := order
					request := *order.InputRequest
					updated.Version++
					request.Status, request.Answer = "answered", body.Answer
					updated.InputRequest = &request
					addition, _ := json.Marshal(map[string]string{"request_id": body.RequestID, "question": request.Question, "answer": body.Answer})
					updated.BuyerInput += commissionInputClarificationSeparator + string(addition)
					if scenario == "invalid_receipt" {
						updated.BuyerInput = "unrelated"
					}
					materialTestResponse(out, map[string]any{"order": updated})
					return
				}
				t.Errorf("unexpected API %s %s", r.Method, r.URL.Path)
				http.NotFound(out, r)
			}))
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			installCommissionIntakeRules(t, w)
			job := claimBuyerClarification(t, w, 4)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w.runAgent = func(ctx context.Context, request dispatch.Request) (dispatch.Result, error) {
				ran = true
				if request.Kind != "commission_clarification" || request.SessionID != "" || strings.Contains(request.Prompt, "test-v2") || !strings.Contains(request.Prompt, "finance team") {
					t.Error("unsafe clarification runner request")
				}
				if scenario == "run_error" {
					return dispatch.Result{}, errors.New("interrupted")
				}
				if scenario == "invalid_result" {
					return dispatch.Result{Text: "answer"}, nil
				}
				if scenario == "changed_identity" {
					cred, _ := auth.LoadV2Credentials(w.server.Name)
					cred.AgentID = "43"
					_ = auth.SaveV2Credentials(w.server.Name, cred)
				}
				if scenario == "cancelled" {
					cancel()
				}
				decision := dispatch.CommissionClarificationDecision{Version: 1, RequestID: request.ID, OrderID: "901", OrderVersion: 4, InputRequestID: order.InputRequest.RequestID, Outcome: "answered", Answer: answer, Summary: "Read the supplied buyer input"}
				if scenario == "waiting" {
					decision.Outcome, decision.Answer = "waiting", ""
				}
				raw, _ := json.Marshal(decision)
				return dispatch.Result{Text: string(raw)}, nil
			}
			if err := w.dispatchCommissionClarification(ctx, job); err != nil {
				t.Fatal(err)
			}
			var saved dispatch.Job
			for _, candidate := range w.journal.Snapshot() {
				if candidate.ID == job.ID {
					saved = candidate
				}
			}
			wantWrites := 0
			if scenario == "answered" || scenario == "multiline_answer" || scenario == "uncertain_write" || scenario == "invalid_receipt" {
				wantWrites = 1
			}
			if writes != wantWrites {
				t.Fatalf("writes=%d", writes)
			}
			if (scenario == "answered" || scenario == "multiline_answer") && saved.Code != "commission_clarification_answered" {
				t.Fatalf("answer not confirmed %+v", saved)
			}
			if scenario == "waiting" && saved.Code != "commission_clarification_waiting_facts" || scenario == "missing_files" && (saved.Code != "commission_clarification_waiting_files" || ran) {
				t.Fatalf("waiting became a user action or fabricated answer: %+v", saved)
			}
			if scenario == "run_error" || scenario == "uncertain_write" || scenario == "invalid_receipt" || scenario == "changed_identity" || scenario == "cancelled" {
				if saved.Status != "unknown" {
					t.Fatalf("uncertain answer not fenced %+v", saved)
				}
			}
			if _, ok, err := w.journal.NextKind("commission_clarification"); err != nil || ok {
				t.Fatalf("answer auto replay: %v %v", ok, err)
			}
		})
	}
}

func TestCommissionClarificationSellerWaitsAndBoundsRounds(t *testing.T) {
	for _, scenario := range []string{"request", "waiting", "round_limit", "missing_files"} {
		t.Run(scenario, func(t *testing.T) {
			order := testCommissionOrder()
			order.State = "awaiting_seller"
			if scenario == "waiting" || scenario == "round_limit" {
				order.InputRequest = &commissionInputRequest{RequestID: strings.Repeat("a", 64), Question: "Which scope?", Round: 1, Status: "waiting_buyer"}
				if scenario == "round_limit" {
					order.InputRequest.Status, order.InputRequest.Answer, order.InputRequest.Round = "answered", "Known scope", 3
				}
			}
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/orders/901/request-inputs" || r.Method != http.MethodPost {
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					return
				}
				writes++
				var body struct {
					Version   int64    `json:"expected_version"`
					RequestID string   `json:"request_id"`
					Question  string   `json:"question"`
					Missing   []string `json:"missing_inputs"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body.Version != 3 || len(body.RequestID) != 64 || len(r.Header.Get(idempotencyHeader)) != 64 {
					t.Error("invalid request fence")
				}
				if scenario == "missing_files" && len(body.Missing) != 1 {
					t.Error("missing-file request was text-only")
				}
				updated := order
				updated.Version++
				updated.InputRequest = &commissionInputRequest{RequestID: body.RequestID, Question: body.Question, Round: 1, Status: "waiting_buyer", MissingInputs: body.Missing}
				materialTestResponse(out, map[string]any{"order": updated})
			}))
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			installCommissionIntakeRules(t, w)
			job := claimCommissionIntake(t, w)
			missing := []string{}
			if scenario == "missing_files" {
				missing = []string{"required_input_files"}
			}
			if err := w.requestCommissionInputs(context.Background(), job, order, "Which scope?", "", missing...); err != nil {
				t.Fatal(err)
			}
			want := 1
			if scenario == "waiting" || scenario == "round_limit" {
				want = 0
			}
			if writes != want {
				t.Fatalf("duplicate or unbounded request: %d", writes)
			}
			saved := w.journal.Snapshot()[0]
			if saved.Status != "completed" {
				t.Fatalf("waiting escalated to a human %+v", saved)
			}
		})
	}
}

func TestCommissionClarificationAnsweredVersionRechecksSeller(t *testing.T) {
	order := testCommissionOrder()
	order.State, order.Version = "awaiting_seller", 5
	order.BuyerInput += "\nThe intended audience is finance."
	order.InputRequest = &commissionInputRequest{RequestID: strings.Repeat("a", 64), Question: "Who is the audience?", Round: 1, Status: "answered", Answer: "Finance"}
	ran, accepted := 0, false
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/orders/901":
			materialTestResponse(out, map[string]any{"order": order})
		case "/api/v2/console/trade/orders/901":
			materialTestResponse(out, map[string]any{"order_id": "901", "role": "seller", "version": 5, "state": "awaiting_seller", "counterparty": map[string]string{"agent_id": "41"}, "files": map[string]any{"input": []any{}}})
		case "/api/v1/orders/901/accept":
			accepted = true
			current := order
			current.Version++
			current.State = "pending_payment"
			materialTestResponse(out, map[string]any{"order": current})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, w)
	job := claimCommissionIntakeVersion(t, w, 5)
	w.runAgent = func(_ context.Context, request dispatch.Request) (dispatch.Result, error) {
		ran++
		if !strings.Contains(request.Prompt, "finance") {
			t.Error("persisted clarification missing")
		}
		raw, _ := json.Marshal(dispatch.CommissionIntakeDecision{Version: 1, RequestID: request.ID, OrderID: "901", OrderVersion: 5, Outcome: "ready", Summary: "Clarified inputs checked", InspectedFiles: []string{}})
		return dispatch.Result{Text: string(raw)}, nil
	}
	if err := w.dispatchCommissionIntake(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if ran != 1 || !accepted {
		t.Fatal("answered version did not resume seller acceptance")
	}
}

func TestCommissionClarificationMissingFilesAndPendingRequestStayAutomatic(t *testing.T) {
	for _, scenario := range []string{"missing_files", "pending_question"} {
		t.Run(scenario, func(t *testing.T) {
			order := testCommissionOrder()
			order.State = "awaiting_seller"
			order.Contract = json.RawMessage(`{"fulfillment_skill":"input-review","requires_materials":true}`)
			if scenario == "pending_question" {
				order.InputRequest = &commissionInputRequest{RequestID: strings.Repeat("a", 64), Round: 1, Status: "waiting_buyer", Question: "Which audience?", MissingInputs: []string{}}
			}
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/orders/901":
					materialTestResponse(out, map[string]any{"order": order})
				case "/api/v2/console/trade/orders/901":
					materialTestResponse(out, map[string]any{"order_id": "901", "role": "seller", "version": 3, "state": "awaiting_seller", "counterparty": map[string]string{"agent_id": "41"}, "files": map[string]any{"input": []any{}}})
				case "/api/v1/orders/901/request-inputs":
					writes++
					var body struct {
						RequestID string   `json:"request_id"`
						Question  string   `json:"question"`
						Missing   []string `json:"missing_inputs"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					if len(body.Missing) != 1 || body.Missing[0] != "required_input_files" {
						t.Error("missing files were not fenced")
					}
					updated := order
					updated.Version++
					updated.InputRequest = &commissionInputRequest{RequestID: body.RequestID, Question: body.Question, Round: 1, Status: "waiting_buyer", MissingInputs: body.Missing}
					materialTestResponse(out, map[string]any{"order": updated})
				default:
					t.Errorf("unexpected API: %s", r.URL.Path)
				}
			}))
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			installCommissionIntakeRules(t, w)
			job := claimCommissionIntake(t, w)
			w.runAgent = func(context.Context, dispatch.Request) (dispatch.Result, error) {
				t.Error("Agent invoked for known absent files or an already pending request")
				return dispatch.Result{}, nil
			}
			if err := w.dispatchCommissionIntake(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			want := 1
			if scenario == "pending_question" {
				want = 0
			}
			if writes != want {
				t.Fatalf("unexpected request count %d", writes)
			}
			saved := w.journal.Snapshot()[0]
			if saved.Status != "completed" || saved.Code != "commission_clarification_waiting_buyer" {
				t.Fatalf("automatic waiting became a human blocker: %+v", saved)
			}
		})
	}
}
