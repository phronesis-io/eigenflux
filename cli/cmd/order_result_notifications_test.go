package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/desktopqueue"
	"cli.eigenflux.ai/internal/dispatch"
)

var commissionResultTextCases = []struct {
	kind, state, zh, en string
}{
	{"order.review.created.v1", "completed", "订单收到新评价", "New order review received"},
	{"order.refund.processing_error.v1", "refund_pending", "退款处理异常，系统正在重试", "Refund processing issue; automatic retry in progress"},
	{"order.refund.completed.v1", "cancelled", "订单已取消，款项已退回", "Order cancelled; payment refunded"},
	{"order.settlement.processing_error.v1", "completed", "收益入账异常，系统正在重试", "Earnings credit issue; automatic retry in progress"},
	{"order.settlement.completed.v1", "completed", "订单收益已入账", "Order earnings credited to your platform wallet"},
	{"order.validation.processing_error.v1", "validating", "交付检查暂时异常，系统正在重试", "Delivery check temporarily unavailable; automatic retry in progress"},
}

func commissionResultTestNotification(t *testing.T, id, orderID int64, recipient, kind, state string) json.RawMessage {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(commissionTestNotification(id, orderID, recipient), &fields); err != nil {
		t.Fatal(err)
	}
	fields["type"] = kind
	payload := fields["payload"].(map[string]any)
	payload["event_kind"], payload["to_state"], payload["state"] = kind, state, state
	// Server errors and other business data must never become notification copy.
	payload["last_error"] = "private provider error with token=secret"
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCommissionResultNotificationTerminalAndDesktopBoundaries(t *testing.T) {
	for _, tc := range commissionResultTextCases {
		t.Run(tc.kind, func(t *testing.T) {
			raw := commissionResultTestNotification(t, 81, 9007199254740995, "9007199254740997", tc.kind, tc.state)
			var notification orderNotification
			if err := json.Unmarshal(raw, &notification); err != nil {
				t.Fatal(err)
			}
			for _, language := range []string{"zh", "en"} {
				want := tc.en
				if language == "zh" {
					want = tc.zh
				}
				var output strings.Builder
				if err := renderOrderNotification(&output, "table", language, notification); err != nil {
					t.Fatal(err)
				}
				if got := output.String(); !strings.Contains(got, want) || !strings.Contains(got, "#9007199254740995 v3") || strings.Contains(got, "secret") {
					t.Fatalf("unexpected %s output: %s", language, got)
				}
				// Older envelopes do not include payload.event_kind.
				legacy := notification.Payload
				legacy.EventKind = ""
				if _, got := localizedOrderNotificationText(language, tc.kind, legacy); got != want {
					t.Fatalf("type fallback: %s", got)
				}
				if _, got := localizedOrderNotificationText(language, "order.state.changed.v1", notification.Payload); got != want {
					t.Fatalf("event_kind priority: %s", got)
				}
			}
			message, err := commissionDesktopMessage(raw, "https://example.com", "9007199254740997", "zh")
			if err != nil || !strings.Contains(message.Title, desktopServiceNamePlaceholder) || message.Body == "" || strings.Contains(message.Title+message.Body, "secret") {
				t.Fatalf("unexpected desktop text: %+v %v", message, err)
			}
			target, _ := url.Parse(message.URL)
			if target.Query().Get("agent_id") != "9007199254740997" || target.Query().Get("order_id") != "9007199254740995" || target.Query().Get("role") != "seller" {
				t.Fatal("lost account/order identity", message.URL)
			}
			if _, err := commissionDesktopMessage(raw, "https://example.com", "9007199254740998", "zh"); err == nil {
				t.Fatal("result notice crossed accounts")
			}
		})
	}
}

func TestCommissionNotificationTextPriorityAndRejection(t *testing.T) {
	payload := orderNotificationPayload{RecipientRole: "buyer", ToState: "cancelled", CommandKind: "reject_order"}
	for _, language := range []string{"zh", "en"} {
		want := "Seller declined the order"
		if language == "zh" {
			want = "商家已拒单"
		}
		if _, got := localizedOrderNotificationText(language, "order.state.changed.v1", payload); got != want {
			t.Fatalf("rejection used cancellation text: %s", got)
		}
	}
	payload.EventKind = "order.refund.completed.v1"
	if _, got := localizedOrderNotificationText("zh", "order.settlement.completed.v1", payload); got != "订单已取消，款项已退回" {
		t.Fatalf("event_kind must precede envelope type and command: %s", got)
	}
	payload.EventKind, payload.CommandKind = "future.event.v1", ""
	if _, got := localizedOrderNotificationText("zh", "future.event.v1", payload); got != "订单已取消" {
		t.Fatalf("unknown types must retain state fallback: %s", got)
	}
}

func TestCommissionResultIntakeNeverCallsAgentOrOrderAPI(t *testing.T) {
	for _, tc := range commissionResultTextCases {
		for _, field := range []string{"type", "event_kind"} {
			t.Run(tc.kind+"/"+field, func(t *testing.T) {
				var requests, runs atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					t.Errorf("result notification made HTTP request: %s %s", r.Method, r.URL.Path)
					http.Error(out, "unexpected request", http.StatusInternalServerError)
				}))
				defer server.Close()
				w := newCommissionWatch(t, server.URL, "commission_order")
				w.runAgent = func(context.Context, dispatch.Request) (dispatch.Result, error) {
					runs.Add(1)
					return dispatch.Result{}, nil
				}
				// An actionable state must not make these informational events run work.
				raw := commissionResultTestNotification(t, 81, 901, "42", tc.kind, "in_progress")
				var fields map[string]any
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				if field == "type" {
					delete(fields["payload"].(map[string]any), "event_kind")
				} else {
					fields["type"] = "order.state.changed.v1"
				}
				raw, _ = json.Marshal(fields)
				if err := w.journal.AddCommissionNotification(raw); err != nil {
					t.Fatal(err)
				}
				job, ok, err := w.journal.NextKind("commission_order")
				if err != nil || !ok {
					t.Fatalf("claim: %v %v", ok, err)
				}
				if err := w.dispatchCommissionIntake(context.Background(), job); err != nil {
					t.Fatal(err)
				}
				reopened, err := dispatch.OpenJournal(*w.binding)
				if err != nil {
					t.Fatal(err)
				}
				jobs := reopened.Snapshot()
				if requests.Load() != 0 || runs.Load() != 0 || len(jobs) != 1 || jobs[0].Status != "completed" || jobs[0].Code != "commission_result_notification_recorded" {
					t.Fatalf("informational result executed work: requests=%d runs=%d jobs=%+v", requests.Load(), runs.Load(), jobs)
				}
			})
		}
	}
}

func TestCommissionResultDesktopDedupAcrossTransportsRetryAndRestart(t *testing.T) {
	var w *accountWatch
	path := filepath.Join(t.TempDir(), "desktop.json")
	count := 1 + len(commissionResultTextCases)
	var acks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/notifications/ack" {
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(out, r)
			return
		}
		saved, err := desktopqueue.Open(path, w.scope)
		if err != nil {
			t.Error(err)
		} else if pending, delivered := saved.Counts(); pending+delivered != count {
			t.Errorf("ACK preceded complete desktop persistence: %d", pending+delivered)
		}
		acks.Add(1)
		_, _ = io.WriteString(out, `{"code":0,"data":{}}`)
	}))
	defer server.Close()
	w = newCommissionWatch(t, server.URL, "commission_order")
	var err error
	w.desktop, err = desktopqueue.Open(path, w.scope)
	if err != nil {
		t.Fatal(err)
	}
	notifications := []json.RawMessage{commissionTestNotification(80, 9007199254740995, "42")}
	for i, tc := range commissionResultTextCases {
		notifications = append(notifications, commissionResultTestNotification(t, int64(81+i), 9007199254740995, "42", tc.kind, tc.state))
	}
	page := commissionTestPage(notifications...)
	for _, transport := range []string{"socket", "http_poll"} {
		if err := w.deliverCommissionNotifications(context.Background(), page, transport); err != nil {
			t.Fatal(err)
		}
	}
	if jobs := w.journal.Snapshot(); len(jobs) != 1 || jobs[0].Status != "pending" {
		t.Fatalf("changed execution journal deduplication: %+v", jobs)
	}
	now := time.Now()
	entry, ok := w.desktop.Next(now)
	if !ok {
		t.Fatal("missing desktop entry")
	}
	if err := w.desktop.Complete(entry.Message.ID, false, now); err != nil {
		t.Fatal(err)
	}
	w.desktop, err = desktopqueue.Open(path, w.scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := desktopqueue.Open(path, "different-account-scope"); err == nil {
		t.Fatal("opened result queue under another account")
	}
	if err := w.deliverCommissionNotifications(context.Background(), page, "http_poll"); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for {
		entry, ok := w.desktop.Next(now.Add(2 * time.Minute))
		if !ok {
			break
		}
		if seen[entry.Message.ID] {
			t.Fatal("duplicate result notification", entry.Message.ID)
		}
		seen[entry.Message.ID] = true
		if err := w.desktop.Complete(entry.Message.ID, true, now); err != nil {
			t.Fatal(err)
		}
	}
	w.desktop, err = desktopqueue.Open(path, w.scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.deliverCommissionNotifications(context.Background(), page, "socket"); err != nil {
		t.Fatal(err)
	}
	if pending, delivered := w.desktop.Counts(); pending != 0 || delivered != count || len(seen) != count || acks.Load() != 4 {
		t.Fatalf("duplicate/lost delivery: pending=%d delivered=%d seen=%d acks=%d", pending, delivered, len(seen), acks.Load())
	}
}
