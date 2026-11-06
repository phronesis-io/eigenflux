package cmd

import (
	"cli.eigenflux.ai/internal/dispatch"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithdrawInputRequestReinspectionAcceptsOnlyAfterReady(t *testing.T) {
	for _, outcome := range []string{"ready", "needs_user"} {
		t.Run(outcome, func(t *testing.T) {
			order := testCommissionOrder()
			order.State = "awaiting_seller"
			order.Version = 2
			requestID := strings.Repeat("a", 64)
			order.InputRequest = &commissionInputRequest{RequestID: requestID, Round: 1, Status: "waiting_buyer", Question: "Which scope?", MissingInputs: []string{}}
			var withdrew, ran, accepted bool
			var keys []string
			var receipt commissionIntakeOrder
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-v2" {
					t.Error("unpinned credentials")
				}
				switch r.URL.Path {
				case "/api/v1/orders/901/withdraw-input-request":
					var body struct {
						Version   int64  `json:"expected_version"`
						RequestID string `json:"request_id"`
						Reason    string `json:"reason"`
					}
					if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Version != 2 || body.RequestID != requestID || body.Reason != "Broad animal group" {
						t.Error("invalid withdrawal command")
					}
					keys = append(keys, r.Header.Get(idempotencyHeader))
					if !withdrew {
						order.Version++
						order.InputRequest.Status = "withdrawn"
						receipt = order
						withdrew = true
					}
					materialTestResponse(out, map[string]any{"order": receipt})
				case "/api/v1/orders/901":
					materialTestResponse(out, map[string]any{"order": order})
				case "/api/v2/console/trade/orders/901":
					materialTestResponse(out, map[string]any{"order_id": "901", "role": "seller", "version": order.Version, "state": order.State, "counterparty": map[string]string{"agent_id": "41"}, "files": map[string]any{"input": []any{}}})
				case "/api/v1/orders/901/accept":
					if !ran || outcome != "ready" {
						t.Error("acceptance bypassed inspection")
					}
					accepted = true
					order.State = "pending_payment"
					order.Version++
					materialTestResponse(out, map[string]any{"order": order})
				default:
					t.Errorf("unexpected %s", r.URL.Path)
					http.NotFound(out, r)
				}
			}))
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			installCommissionIntakeRules(t, w)
			command := newOrderWithdrawInputRequestCommand()
			for k, v := range map[string]string{"expected-version": "2", "request-id": requestID, "reason": "Broad animal group"} {
				if err := command.Flags().Set(k, v); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				if err := command.RunE(command, []string{"901"}); err != nil {
					t.Fatal(err)
				}
			}
			if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
				t.Fatal("withdrawal replay changed idempotency key")
			}
			if order.InputRequest.Answer != "" || order.SnapshotID != 902 || order.State != "awaiting_seller" {
				t.Fatal("withdrawal changed buyer facts or accepted prematurely")
			}
			w.runAgent = func(_ context.Context, r dispatch.Request) (dispatch.Result, error) {
				ran = true
				if !strings.Contains(r.Prompt, `"status":"withdrawn"`) {
					t.Error("inspection lacks withdrawn clarification context")
				}
				raw, _ := json.Marshal(dispatch.CommissionIntakeDecision{Version: 1, RequestID: r.ID, OrderID: "901", OrderVersion: 3, Outcome: outcome, Summary: "Original animal name identifies a broad group", InspectedFiles: []string{}})
				return dispatch.Result{Text: string(raw)}, nil
			}
			if err := w.dispatchCommissionIntake(context.Background(), claimCommissionIntake(t, w)); err != nil {
				t.Fatal(err)
			}
			if !ran || accepted != (outcome == "ready") {
				t.Fatalf("ran=%v accepted=%v", ran, accepted)
			}
		})
	}
}

func TestWithdrawInputRequestRejectsInvalidFlags(t *testing.T) {
	for _, tc := range []struct{ version, request string }{{"0", strings.Repeat("a", 64)}, {"1", ""}, {"1", strings.Repeat("A", 64)}, {"1", "abc"}} {
		c := newOrderWithdrawInputRequestCommand()
		_ = c.Flags().Set("expected-version", tc.version)
		_ = c.Flags().Set("request-id", tc.request)
		if err := c.RunE(c, []string{"901"}); err == nil {
			t.Fatal("invalid flags accepted")
		}
	}
}

func TestWithdrawnClarificationRequiresEmptyAnswer(t *testing.T) {
	r := &commissionInputRequest{RequestID: strings.Repeat("a", 64), Round: 1, Status: "withdrawn", Question: "Which scope?"}
	if !validCommissionInputRequest(r) {
		t.Fatal("withdrawn request invalid")
	}
	r.Answer = "Invented buyer answer"
	if validCommissionInputRequest(r) {
		t.Fatal("withdrawal admitted fabricated answer")
	}
}

func TestWithdrawInputRequestRejectsUnconfirmedReceipt(t *testing.T) {
	for _, scenario := range []string{"wrong_seller", "wrong_version", "still_waiting", "fabricated_answer", "wrong_request", "server_rejected"} {
		t.Run(scenario, func(t *testing.T) {
			requestID := strings.Repeat("a", 64)
			order := testCommissionOrder()
			order.State = "awaiting_seller"
			order.Version = 3
			order.InputRequest = &commissionInputRequest{RequestID: requestID, Round: 1, Status: "withdrawn", Question: "Which scope?"}
			switch scenario {
			case "wrong_seller":
				order.SellerID = 43
			case "wrong_version":
				order.Version = 4
			case "still_waiting":
				order.InputRequest.Status = "waiting_buyer"
			case "fabricated_answer":
				order.InputRequest.Answer = "Buyer said species"
			case "wrong_request":
				order.InputRequest.RequestID = strings.Repeat("b", 64)
			}
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if scenario == "server_rejected" {
					_ = json.NewEncoder(out).Encode(map[string]any{"code": 409, "msg": "ORDER_VERSION_CONFLICT"})
					return
				}
				materialTestResponse(out, map[string]any{"order": order})
			}))
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			installCommissionIntakeRules(t, w)
			c := newOrderWithdrawInputRequestCommand()
			_ = c.Flags().Set("expected-version", "2")
			_ = c.Flags().Set("request-id", requestID)
			if err := c.RunE(c, []string{"901"}); err == nil {
				t.Fatal("invalid receipt accepted")
			}
		})
	}
}
