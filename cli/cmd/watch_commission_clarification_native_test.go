package cmd

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/dispatch"
	watchstate "cli.eigenflux.ai/internal/watch"
)

// Explicit opt-in: two fresh local Codex sessions use only fixture order text
// and localhost APIs. This does not test desktop main-thread or hidden input.
func TestCommissionNativeCodexTextClarification(t *testing.T) {
	if os.Getenv("EIGENFLUX_COMMISSION_CLARIFICATION_NATIVE_SMOKE") != "1" {
		t.Skip("set EIGENFLUX_COMMISSION_CLARIFICATION_NATIVE_SMOKE=1 for real Codex clarification")
	}
	executable, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	order := paidCommissionTestOrder()
	order.BuyerID, order.SellerID = 42, 41
	order.State, order.Version = "awaiting_seller", 2
	order.BuyerInput = "The report title must be Orchid Launch. Use Simplified Chinese for the report."
	order.InputRequest = &commissionInputRequest{RequestID: strings.Repeat("d", 64), Round: 1, Status: "waiting_buyer", Question: "What exact report title should be used? Clarify it from the authorized buyer text.", MissingInputs: []string{}, RequestedAt: time.Now().UnixMilli()}
	var mu sync.Mutex
	var supplements, accepts int
	var acceptedAnswer string
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orders/901":
			materialTestResponse(out, map[string]any{"order": order})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/console/trade/orders/901":
			materialTestResponse(out, map[string]any{"order_id": "901", "role": "seller", "version": order.Version, "state": order.State, "counterparty": map[string]string{"agent_id": "42"}, "files": map[string]any{"input": []any{}}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orders/901/supplement-inputs":
			var body struct {
				Version   int64  `json:"expected_version"`
				RequestID string `json:"request_id"`
				Answer    string `json:"answer"`
			}
			key, keyErr := hex.DecodeString(r.Header.Get(idempotencyHeader))
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Version != order.Version || body.RequestID != order.InputRequest.RequestID || order.InputRequest.Status != "waiting_buyer" || !strings.Contains(body.Answer, "Orchid Launch") || keyErr != nil || len(key) != 32 {
				t.Error("native buyer did not submit the exact fenced factual answer")
				http.Error(out, "invalid supplement", http.StatusConflict)
				return
			}
			supplements++
			acceptedAnswer = body.Answer
			record, _ := json.Marshal(map[string]string{"request_id": order.InputRequest.RequestID, "question": order.InputRequest.Question, "answer": body.Answer})
			order.BuyerInput += "\n\n[EIGENFLUX ORDER INPUT CLARIFICATION]\n" + string(record)
			order.InputRequest.Status, order.InputRequest.Answer = "answered", body.Answer
			order.InputRequest.AnsweredAt = time.Now().UnixMilli()
			order.Version++
			materialTestResponse(out, map[string]any{"order": order})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orders/901/accept":
			var body struct {
				Version int64 `json:"expected_version"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Version != order.Version || order.State != "awaiting_seller" || order.InputRequest.Status != "answered" || r.Header.Get(idempotencyHeader) == "" {
				t.Error("seller accepted before observing the persisted answer")
				http.Error(out, "invalid acceptance", http.StatusConflict)
				return
			}
			accepts++
			order.State, order.Version = "pending_payment", order.Version+1
			materialTestResponse(out, map[string]any{"order": order})
		default:
			t.Errorf("unexpected fixture API: %s %s", r.Method, r.URL.Path)
			http.NotFound(out, r)
		}
	}))
	defer server.Close()
	configure := func(w *accountWatch) {
		installCommissionIntakeRules(t, w)
		w.binding.Mode, w.binding.Host = "native", "codex"
		w.binding.Command = []string{executable}
		w.binding.Args = []string{"--sandbox", "workspace-write", "--ask-for-approval", "never"}
		w.binding.TimeoutSeconds = 180
		if err := dispatch.WriteJSON(dispatch.BindingPath(w.home, w.server.Name), *w.binding); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command("git", "init", "--quiet", w.binding.WorkDir).CombinedOutput(); err != nil {
			t.Fatalf("isolated Git workspace: %v %s", err, output)
		}
		runner := dispatch.Runner{Binding: *w.binding}
		w.runAgent = func(ctx context.Context, request dispatch.Request) (dispatch.Result, error) {
			if request.SessionID != "" {
				t.Fatal("native clarification smoke must use a fresh session")
			}
			return runner.Run(ctx, request)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	buyer := newCommissionWatch(t, server.URL, "commission_order")
	configure(buyer)
	var notice map[string]any
	if err := json.Unmarshal(commissionTestNotification(81, 901, "42"), &notice); err != nil {
		t.Fatal(err)
	}
	payload := notice["payload"].(map[string]any)
	payload["recipient_role"], payload["order_version"] = "buyer", 2
	noticeRaw, _ := json.Marshal(notice)
	if err := buyer.journal.AddCommissionNotification(noticeRaw); err != nil {
		t.Fatal(err)
	}
	source, ok, err := buyer.journal.NextKind("commission_order")
	if err != nil || !ok {
		t.Fatal("buyer source notice missing")
	}
	if err := buyer.queueBuyerCommissionClarification(ctx, source, "901"); err != nil {
		t.Fatal(err)
	}
	answerJob, ok, err := buyer.journal.NextKind("commission_clarification")
	if err != nil || !ok {
		t.Fatal("buyer clarification was not queued")
	}
	started := time.Now()
	if err := buyer.dispatchCommissionClarification(ctx, answerJob); err != nil {
		t.Fatal(err)
	}
	var buyerSession string
	for _, saved := range buyer.journal.Snapshot() {
		if saved.ID == answerJob.ID {
			if saved.Status != "completed" || saved.Code != "commission_clarification_answered" || saved.SessionID == "" {
				t.Fatalf("native buyer answer was not confirmed: status=%s code=%s", saved.Status, saved.Code)
			}
			buyerSession = saved.SessionID
		}
	}
	if buyerSession == "" {
		t.Fatal("native buyer session receipt missing")
	}
	t.Logf("real Codex buyer clarification: %.3fs; session=%s", time.Since(started).Seconds(), buyerSession)
	mu.Lock()
	var recorded struct {
		RequestID string `json:"request_id"`
		Question  string `json:"question"`
		Answer    string `json:"answer"`
	}
	_, recordedRaw, hasRecord := strings.Cut(order.BuyerInput, "\n\n[EIGENFLUX ORDER INPUT CLARIFICATION]\n")
	answered := supplements == 1 && accepts == 0 && order.Version == 3 && order.InputRequest.Status == "answered" && order.InputRequest.Answer == acceptedAnswer && hasRecord && json.Unmarshal([]byte(recordedRaw), &recorded) == nil && recorded.RequestID == order.InputRequest.RequestID && recorded.Question == order.InputRequest.Question && recorded.Answer == acceptedAnswer
	mu.Unlock()
	if !answered {
		t.Fatal("local fixture did not persist the answered request before seller inspection")
	}
	// Change only the independent fixture account. Real Codex authentication is
	// host-owned; no previous chat, EigenFlux account or live order is reused.
	seller := newCommissionWatch(t, server.URL, "commission_order")
	seller.identity.AgentID, seller.identity.PrincipalID = "41", "principal-41"
	if err := auth.SaveV2Credentials(seller.server.Name, &seller.identity); err != nil {
		t.Fatal(err)
	}
	seller.binding.AgentID, seller.binding.PrincipalID = seller.identity.AgentID, seller.identity.PrincipalID
	seller.scope = watchstate.Scope(seller.home, seller.server.Name, seller.identity.AgentID, seller.identity.PrincipalID)
	seller.binding.Scope = seller.scope
	seller.journal, err = dispatch.OpenJournal(*seller.binding)
	if err != nil {
		t.Fatal(err)
	}
	configure(seller)
	if err := seller.journal.AddCommissionNotification(commissionTestNotification(82, 901, "41")); err != nil {
		t.Fatal(err)
	}
	inspection, ok, err := seller.journal.NextKind("commission_order")
	if err != nil || !ok {
		t.Fatal("seller reinspection missing")
	}
	started = time.Now()
	if err := seller.dispatchCommissionIntake(ctx, inspection); err != nil {
		t.Fatal(err)
	}
	var sellerSession string
	for _, saved := range seller.journal.Snapshot() {
		if saved.ID == inspection.ID {
			if saved.CommissionResult == nil || saved.CommissionResult.Outcome != "ready" || saved.CommissionResult.OrderVersion != 3 {
				t.Fatalf("native seller did not pass reinspection: status=%s code=%s", saved.Status, saved.Code)
			}
			sellerSession = saved.SessionID
		}
	}
	mu.Lock()
	accepted := supplements == 1 && accepts == 1 && order.State == "pending_payment" && order.Version == 4
	mu.Unlock()
	if !accepted || sellerSession == "" || sellerSession == buyerSession {
		t.Fatal("local clarification did not reach independently confirmed seller readiness")
	}
	t.Logf("real Codex seller reinspection: %.3fs; session=%s; fixture_state=pending_payment; supplements=1 accepts=1; no real order or payment", time.Since(started).Seconds(), sellerSession)
}
