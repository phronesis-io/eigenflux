package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/dispatch"
)

func TestBuyerMissingFilesEscalatesOnceAndOnlyWhileCurrent(t *testing.T) {
	order := clarificationBuyerOrder()
	order.InputRequest.MissingInputs = []string{"inputs/brief.pdf"}
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/orders/901" {
			t.Errorf("unexpected mutation %s %s", r.Method, r.URL.Path)
			http.NotFound(out, r)
			return
		}
		materialTestResponse(out, map[string]any{"order": order})
	}))
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, w)
	if err := w.enableDesktop(); err != nil {
		t.Fatal(err)
	}
	job := claimBuyerClarification(t, w, order.Version)
	w.runAgent = func(context.Context, dispatch.Request) (dispatch.Result, error) {
		t.Error("missing files ran a guessing Agent")
		return dispatch.Result{}, nil
	}
	if err := w.dispatchCommissionClarification(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if pending, _ := w.desktop.Counts(); pending != 1 {
		t.Fatalf("desktop duplicate/missing: %d", pending)
	}
	entry, ok := w.desktop.Next(time.Now())
	if !ok || !strings.Contains(entry.Message.Body, "inputs/brief.pdf") {
		t.Fatal("missing gap", entry)
	}
	u, _ := url.Parse(entry.Message.URL)
	if u.Query().Get("agent_id") != "42" || u.Query().Get("order_id") != "901" || u.Query().Get("role") != "buyer" {
		t.Fatal("lost identity", u)
	}
	q, err := dispatch.OpenMainNoticeQueue(*w.binding)
	if err != nil {
		t.Fatal(err)
	}
	item, ok, err := q.Next()
	if err != nil || !ok || !item.IsBuyerInputRequest() || item.InputRequestID != order.InputRequest.RequestID {
		t.Fatal(item, ok, err)
	}
	if _, ok, err := q.Next(); err != nil || ok {
		t.Fatal("duplicate main notice", ok, err)
	}
	calls := 0
	w.runMainNotice = func(_ context.Context, prompt string) (dispatch.Result, error) {
		calls++
		if !strings.Contains(prompt, `"input_request_id":"`+order.InputRequest.RequestID+`"`) || !strings.Contains(prompt, `"order_url"`) || strings.Contains(prompt, "test-v2") {
			t.Error("invalid main request")
		}
		return dispatch.Result{Text: "Please add the brief on the order page."}, nil
	}
	status, code := w.dispatchCommissionMainNotice(context.Background(), item)
	if status != "delivered" || calls != 1 {
		t.Fatal(status, code, calls)
	}
	current, err := w.buyerMaterialDesktopCurrent(context.Background(), entry.Message.ID)
	if err != nil || !current {
		t.Fatal(current, err)
	}
	w.runMainNotice = func(context.Context, string) (dispatch.Result, error) {
		calls++
		order.InputRequest.Status, order.InputRequest.Answer = "answered", "Added the brief."
		order.Version++
		return dispatch.Result{Text: "An already admitted host turn can finish."}, nil
	}
	status, code = w.dispatchCommissionMainNotice(context.Background(), item)
	if status != "skipped" || calls != 2 {
		t.Fatalf("request answered during call marked delivered: %s %s calls=%d", status, code, calls)
	}
	status, code = w.dispatchCommissionMainNotice(context.Background(), item)
	current, err = w.buyerMaterialDesktopCurrent(context.Background(), entry.Message.ID)
	if status != "skipped" || calls != 2 || err != nil || current {
		t.Fatalf("resolved request surfaced: %s %s %d %v %v", status, code, calls, current, err)
	}
}

func TestBuyerDesktopMaterialReminderRechecksAfterTitleLookup(t *testing.T) {
	order := clarificationBuyerOrder()
	order.Contract = json.RawMessage(`{"title":"Report","fulfillment_skill":"input-review","requires_materials":false}`)
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/orders/901" {
			materialTestResponse(out, map[string]any{})
			return
		}
		reads++
		if reads == 2 {
			order.Version++
			order.InputRequest.Status, order.InputRequest.Answer = "answered", "Provided the facts"
		}
		materialTestResponse(out, map[string]any{"order": order})
	}))
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, w)
	if err := w.enableDesktop(); err != nil {
		t.Fatal(err)
	}
	data := dispatch.CommissionClarificationJob{OrderID: "901", OrderVersion: order.Version, RequestID: order.InputRequest.RequestID}
	target, err := orderDesktopURL(w.server.Endpoint, w.identity.AgentID, "901", "buyer")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.desktop.Enqueue(desktopnotify.Message{ID: buyerMaterialDesktopID(w.scope, data), Title: "「{service_name}」需要你补充资料", Body: "请补充资料", URL: target}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider := &fakeDesktopProvider{setup: func() error { return nil }}
	w.desktopProvider = provider
	w.emit = func(kind string, value interface{}) error {
		if v, ok := value.(map[string]string); kind == "desktop_notification_status" && ok && v["status"] == "skipped" {
			cancel()
		}
		return nil
	}
	if err := w.desktopLoop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if provider.calls != 0 || reads != 2 {
		t.Fatalf("stale request shown after title lookup: shows=%d reads=%d", provider.calls, reads)
	}
}

func TestBuyerMaterialDesktopCanDrainQueueWithoutExecutionBinding(t *testing.T) {
	order := clarificationBuyerOrder()
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		materialTestResponse(out, map[string]any{"order": order})
	}))
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, w)
	if err := os.Remove(dispatch.BindingPath(w.home, w.server.Name)); err != nil {
		t.Fatal(err)
	}
	w.notificationsOnly, w.binding = true, nil
	if err := w.enableNotificationsOnly(); err != nil {
		t.Fatal(err)
	}
	data := dispatch.CommissionClarificationJob{OrderID: "901", OrderVersion: order.Version, RequestID: order.InputRequest.RequestID}
	current, err := w.buyerMaterialDesktopCurrent(context.Background(), buyerMaterialDesktopID(w.scope, data))
	if err != nil || !current {
		t.Fatal(current, err)
	}
}

func TestBuyerUnknownFactEscalatesButAutomaticAnswerStaysSilent(t *testing.T) {
	for _, outcome := range []string{"waiting", "answered"} {
		t.Run(outcome, func(t *testing.T) {
			order := clarificationBuyerOrder()
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/orders/901":
					materialTestResponse(out, map[string]any{"order": order})
				case "/api/v2/console/trade/orders/901":
					materialTestResponse(out, buyerClarificationTestDetail(order, []map[string]any{}))
				case "/api/v1/orders/901/supplement-inputs":
					var body struct {
						Answer string `json:"answer"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					addition, _ := json.Marshal(map[string]string{"request_id": order.InputRequest.RequestID, "question": order.InputRequest.Question, "answer": body.Answer})
					order.BuyerInput += commissionInputClarificationSeparator + string(addition)
					order.Version++
					order.InputRequest.Status, order.InputRequest.Answer = "answered", body.Answer
					materialTestResponse(out, map[string]any{"order": order})
				default:
					t.Errorf("unexpected %s", r.URL.Path)
					http.NotFound(out, r)
				}
			}))
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			installCommissionIntakeRules(t, w)
			if err := w.enableDesktop(); err != nil {
				t.Fatal(err)
			}
			job := claimBuyerClarification(t, w, order.Version)
			w.runAgent = func(_ context.Context, request dispatch.Request) (dispatch.Result, error) {
				decision := dispatch.CommissionClarificationDecision{Version: 1, RequestID: request.ID, OrderID: "901", OrderVersion: order.Version, InputRequestID: order.InputRequest.RequestID, Outcome: outcome, Summary: "Checked supplied facts"}
				if outcome == "answered" {
					decision.Answer = "Finance"
				}
				raw, _ := json.Marshal(decision)
				return dispatch.Result{Text: string(raw)}, nil
			}
			if err := w.dispatchCommissionClarification(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			pending, _ := w.desktop.Counts()
			if outcome == "waiting" && (pending != 1 || w.mainNotices == nil) {
				t.Fatal("human gap stayed silent")
			}
			if outcome == "answered" && (pending != 0 || w.mainNotices != nil) {
				t.Fatal("automatic clarification interrupted the buyer")
			}
		})
	}
}
