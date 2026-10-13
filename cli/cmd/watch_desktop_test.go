package cmd

import (
	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/desktopnotify"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"cli.eigenflux.ai/internal/desktopqueue"
	"cli.eigenflux.ai/internal/dispatch"
)

func TestDesktopOrderLinkKeepsExactIdentityAndRejectsUnsafeOrigins(t *testing.T) {
	target, err := orderDesktopURL("https://www.eigenflux.ai/", "9007199254740995", "9223372036854775807", "buyer")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(target)
	if u.Path != "/dashboard/notifications/open" || u.Query().Get("agent_id") != "9007199254740995" || u.Query().Get("order_id") != "9223372036854775807" || len(u.Query()) != 3 {
		t.Fatal(target)
	}
	for _, origin := range []string{"javascript:alert(1)", "http://example.com", "http://127.0.0.2", "https://user:pass@example.com", "https://example.com?token=secret"} {
		if _, err := orderDesktopURL(origin, "42", "1", "buyer"); err == nil {
			t.Fatal(origin)
		}
	}
	for _, id := range []string{"0", "-1", "01", "9007199254740995x"} {
		if _, err := orderDesktopURL("https://example.com", id, "1", "buyer"); err == nil {
			t.Fatal(id)
		}
	}
}
func TestDesktopAllStagesAndSameVersionReminder(t *testing.T) {
	states := []string{"preparing_materials", "awaiting_seller", "pending_payment", "in_progress", "validating", "awaiting_buyer_confirmation", "completed", "cancelled", "refund_pending", "refunded"}
	for _, state := range states {
		raw := strings.ReplaceAll(string(commissionTestNotification(81, 9007199254740995, "42")), "in_progress", state)
		m, err := commissionDesktopMessage(json.RawMessage(raw), "https://example.com", "42", "zh")
		if err != nil || strings.Contains(m.Title, "订单已更新") {
			t.Fatalf("%s: %v %s", state, err, m.Title)
		}
		if !strings.Contains(m.URL, "order_id=9007199254740995") {
			t.Fatal(m.URL)
		}
	}
	raw := strings.ReplaceAll(string(commissionTestNotification(82, 9007199254740995, "42")), "order.state.changed.v1", "order.reminder.buyer_confirmation_24h.v1")
	m, err := commissionDesktopMessage(json.RawMessage(raw), "https://example.com", "42", "zh")
	if err != nil || m.ID != "commission:82" || !strings.Contains(m.Title, "还未确认交付") {
		t.Fatal(m, err)
	}
	if _, err := commissionDesktopMessage(commissionTestNotification(1, 2, "43"), "https://example.com", "42", "zh"); err == nil {
		t.Fatal("accepted wrong recipient")
	}
}

func TestDesktopCopyUsesOneLanguageAndPreservesRouting(t *testing.T) {
	states := []string{"awaiting_payment", "pending_payment", "awaiting_materials", "preparing_materials", "awaiting_seller", "in_progress", "validating", "delivered", "awaiting_buyer_confirmation", "completed", "cancelled", "rejected", "expired", "refunding", "refund_pending", "refunded", "future_state"}
	for _, language := range []string{"en", "fr", "ja", ""} {
		for _, state := range states {
			raw := strings.ReplaceAll(string(commissionTestNotification(81, 901, "42")), "in_progress", state)
			message, err := commissionDesktopMessage(json.RawMessage(raw), "https://example.com", "42", language)
			if err != nil || !strings.Contains(message.Title, desktopServiceNamePlaceholder) || message.Body == "" {
				t.Fatalf("%s/%s: %+v %v", language, state, message, err)
			}
			for _, r := range message.Title + message.Body {
				if unicode.Is(unicode.Han, r) {
					t.Fatalf("mixed-language notification: %+v", message)
				}
			}
			chinese, err := commissionDesktopMessage(json.RawMessage(raw), "https://example.com", "42", "zh")
			if err != nil || !strings.HasPrefix(chinese.Title, "「{service_name}」") || chinese.Body == "" || chinese.ID != message.ID || chinese.URL != message.URL {
				t.Fatalf("localization changed routing: %+v %+v %v", chinese, message, err)
			}
		}
	}
	for _, tc := range commissionResultTextCases {
		raw := commissionResultTestNotification(t, 81, 901, "42", tc.kind, tc.state)
		message, err := commissionDesktopMessage(raw, "https://example.com", "42", "en")
		if err != nil || !strings.Contains(message.Title, desktopServiceNamePlaceholder) || message.Body == "" {
			t.Fatalf("result notification: %+v %v", message, err)
		}
	}
	for _, tt := range []struct{ kind, command, state, summary string }{
		{"order.reminder.buyer_confirmation_24h.v1", "", "awaiting_buyer_confirmation", "“{service_name}” still needs your confirmation"},
		{"order.state.changed.v1", "reject_order", "cancelled", "“{service_name}” declined by the seller"},
	} {
		var fact desktopOrderFact
		if err := json.Unmarshal(commissionTestNotification(81, 901, "42"), &fact); err != nil {
			t.Fatal(err)
		}
		fact.Type, fact.Payload.CommandKind, fact.Payload.ToState, fact.Payload.RecipientRole = tt.kind, tt.command, tt.state, "buyer"
		raw, _ := json.Marshal(fact)
		message, err := commissionDesktopMessage(raw, "https://example.com", "42", "en")
		if err != nil || message.Title != tt.summary || !strings.Contains(message.URL, "role=buyer") {
			t.Fatalf("buyer notification: %+v %v", message, err)
		}
	}
}

func TestDesktopBlockerLanguages(t *testing.T) {
	for _, tt := range []struct{ status, zh, en, zhBody, enBody string }{
		{"needs_user", "需要你处理", "needs your attention", "你的 Agent 处理这笔订单时需要你的介入，点击打开订单处理。", "Your Agent requires your help on this order. Click for further details."},
		{"failed", "自动处理失败", "couldn't be handled automatically", "点击打开订单查看，必要时手动处理，以免订单超时。", "Click to open the order and handle it manually if needed, before it times out."},
		{"unknown", "处理结果待核对", "result needs checking", "无法确认你的 Agent 是否已完成，点击打开订单核对。", "We can't confirm whether your Agent finished. Click to open the order and check."},
	} {
		for _, language := range []string{"zh", "en", "fr", ""} {
			title, body := desktopBlockerText(language, tt.status)
			wantTitle, wantBody := "“{service_name}” "+tt.en, tt.enBody
			if language == "zh" {
				wantTitle, wantBody = "「{service_name}」"+tt.zh, tt.zhBody
			}
			if title != wantTitle || body != wantBody {
				t.Fatalf("%s/%s: %s / %s", language, tt.status, title, body)
			}
		}
	}
}

func TestDesktopPersistedBeforeACKAndDedupAcrossTransports(t *testing.T) {
	var w *accountWatch
	path := filepath.Join(t.TempDir(), "desktop.json")
	var ack atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/notifications/ack" {
			t.Errorf("unexpected %s", r.URL.Path)
			http.NotFound(out, r)
			return
		}
		saved, err := desktopqueue.Open(path, w.scope)
		if err != nil {
			t.Error(err)
		} else if pending, _ := saved.Counts(); pending != 2 {
			t.Errorf("ACK before both desktop notifications persisted: %d", pending)
		}
		ack.Add(1)
		_, _ = io.WriteString(out, `{"code":0,"data":{}}`)
	}))
	defer server.Close()
	w = newCommissionWatch(t, server.URL, "commission_order")
	w.desktop, _ = desktopqueue.Open(path, w.scope)
	a := commissionTestNotification(81, 901, "42")
	b := json.RawMessage(strings.ReplaceAll(string(commissionTestNotification(82, 901, "42")), "order.state.changed.v1", "order.reminder.buyer_confirmation_24h.v1"))
	for _, transport := range []string{"socket", "http_poll"} {
		if err := w.deliverCommissionNotifications(context.Background(), commissionTestPage(a, b), transport); err != nil {
			t.Fatal(err)
		}
	}
	if ack.Load() != 2 {
		t.Fatal(ack.Load())
	}
	if len(w.journal.Snapshot()) != 1 {
		t.Fatal("business dedup semantics changed")
	}
	if pending, _ := w.desktop.Counts(); pending != 2 {
		t.Fatal("desktop conflated same-version reminder")
	}
}
func TestDesktopDiskFailureNeverACKs(t *testing.T) {
	var ack atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		ack.Add(1)
		_, _ = io.WriteString(out, `{"code":0,"data":{}}`)
	}))
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	path := filepath.Join(t.TempDir(), "blocked", "queue.json")
	w.desktop, _ = desktopqueue.Open(path, w.scope)
	if err := os.WriteFile(filepath.Dir(path), []byte("file, not directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := w.deliverCommissionNotifications(context.Background(), commissionTestPage(commissionTestNotification(81, 901, "42")), "socket"); err == nil {
		t.Fatal("ignored disk error")
	}
	if ack.Load() != 0 {
		t.Fatal("ACKed before desktop durability")
	}
}
func TestDesktopLocalBlockerRecoveredOnce(t *testing.T) {
	w := newCommissionWatch(t, "http://127.0.0.1:8090", "commission_order")
	w.desktop, _ = desktopqueue.Open(filepath.Join(t.TempDir(), "desktop.json"), w.scope)
	if err := w.journal.AddCommissionNotification(commissionTestNotification(81, 901, "42")); err != nil {
		t.Fatal(err)
	}
	job, _, _ := w.journal.NextKind("commission_order")
	if err := w.journal.Update(job.ID, "needs_user", "missing_skill", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := w.enqueueDesktopBlockers(); err != nil {
		t.Fatal(err)
	}
	if err := w.enqueueDesktopBlockers(); err != nil {
		t.Fatal(err)
	}
	entry, ok := w.desktop.Next(time.Now())
	wantTitle, _ := desktopBlockerText(desktopnotify.SystemLanguage(), "needs_user")
	if !ok || entry.Message.Title != wantTitle {
		t.Fatal(entry)
	}
	if pending, _ := w.desktop.Counts(); pending != 1 {
		t.Fatal("replayed blocker")
	}
	// Opening the execution journal still preserves the original unresolved state.
	reopened, err := dispatch.OpenJournal(*w.binding)
	if err != nil || reopened.Snapshot()[0].Status != "needs_user" {
		t.Fatal(err)
	}
}

type fakeDesktopProvider struct {
	setup func() error
	calls int
}

func (f *fakeDesktopProvider) Setup(context.Context) error { return f.setup() }
func (f *fakeDesktopProvider) Show(context.Context, desktopnotify.Message) error {
	f.calls++
	return nil
}
func TestDesktopRechecksIdentityAfterPermissionWait(t *testing.T) {
	w := newCommissionWatch(t, "http://127.0.0.1:8090", "commission_order")
	w.desktop, _ = desktopqueue.Open(filepath.Join(t.TempDir(), "desktop.json"), w.scope)
	if err := w.enqueueDesktopCommission(commissionTestNotification(81, 901, "42")); err != nil {
		t.Fatal(err)
	}
	provider := &fakeDesktopProvider{setup: func() error {
		credentials, _ := auth.LoadV2Credentials(w.server.Name)
		credentials.AgentID = "43"
		return auth.SaveV2Credentials(w.server.Name, credentials)
	}}
	w.desktopProvider = provider
	if err := w.desktopLoop(context.Background()); !errors.Is(err, errWatchIdentity) {
		t.Fatalf("identity error: %v", err)
	}
	if provider.calls != 0 {
		t.Fatal("old-account notification shown after setup changed identity")
	}
}
func TestDesktopUnsupportedOriginDoesNotBreakWatch(t *testing.T) {
	w := newCommissionWatch(t, "http://192.168.1.10:8090", "commission_order")
	if err := w.enableDesktop(); err != nil {
		t.Fatal(err)
	}
	if w.desktop != nil || w.desktopDisabledReason != "unsupported_console_origin" {
		t.Fatal("unsafe link enabled")
	}
	if err := w.desktopLoop(context.Background()); err != nil {
		t.Fatal("notification-only failure terminated watch", err)
	}
	if err := w.enqueueDesktopCommission(commissionTestNotification(81, 901, "42")); err != nil {
		t.Fatal("notification-only failure rejects order intake", err)
	}
}

func TestDesktopSetupReportsRecoveryAndPreservesPendingNotifications(t *testing.T) {
	for _, tt := range []struct {
		name         string
		err          error
		code, action string
	}{
		{"ready_without_orders", nil, "", ""},
		{"missing_helper", fmt.Errorf("wrapped: %w", desktopnotify.ErrHelperMissing), "helper_missing", "install_complete_bundle"},
		{"unrequested", desktopnotify.ErrPermissionRequired, "permission_required", "run_notify_setup"},
		{"denied", desktopnotify.ErrPermission, "permission_denied", "open_system_notification_settings"},
		{"timeout", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), "setup_timeout", "check_system_permission_dialog"},
		{"unsupported", desktopnotify.ErrUnsupported, "unsupported_platform", "use_macos_or_windows"},
		{"other", errors.New("private-provider-details"), "setup_failed", "run_notify_setup"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := newCommissionWatch(t, "http://127.0.0.1:8090", "commission_order")
			var err error
			w.desktop, err = desktopqueue.Open(filepath.Join(t.TempDir(), "desktop.json"), w.scope)
			if err != nil {
				t.Fatal(err)
			}
			if tt.err != nil {
				if err := w.enqueueDesktopCommission(commissionTestNotification(81, 901, "42")); err != nil {
					t.Fatal(err)
				}
			}
			setupCalls := 0
			provider := &fakeDesktopProvider{setup: func() error { setupCalls++; return tt.err }}
			w.desktopProvider = provider
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var got map[string]string
			w.emit = func(kind string, value interface{}) error {
				if kind != "desktop_notification_status" {
					t.Fatalf("unexpected event: %s", kind)
				}
				got = value.(map[string]string)
				cancel()
				return nil
			}
			if err := w.desktopLoop(ctx); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			wantStatus := "permission_or_setup_required"
			if tt.err == nil {
				wantStatus = "ready"
			}
			if got["status"] != wantStatus || got["code"] != tt.code || got["action"] != tt.action {
				t.Fatalf("diagnostic: %#v", got)
			}
			data, _ := json.Marshal(got)
			if strings.Contains(string(data), "private-provider-details") {
				t.Fatal("provider data leaked")
			}
			if setupCalls != 1 || provider.calls != 0 {
				t.Fatalf("setup=%d, show=%d", setupCalls, provider.calls)
			}
			if tt.err != nil {
				if pending, _ := w.desktop.Counts(); pending != 1 {
					t.Fatal("setup failure lost notification")
				}
			}
		})
	}
}
