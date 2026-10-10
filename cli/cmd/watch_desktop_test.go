package cmd

import (
	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/desktopnotify"
	"context"
	"encoding/json"
	"errors"
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
		m, err := commissionDesktopMessage(json.RawMessage(raw), "https://example.com", "42")
		if err != nil || strings.Contains(m.Title, "订单已更新") {
			t.Fatalf("%s: %v %s", state, err, m.Title)
		}
		if !strings.Contains(m.URL, "order_id=9007199254740995") {
			t.Fatal(m.URL)
		}
	}
	raw := strings.ReplaceAll(string(commissionTestNotification(82, 9007199254740995, "42")), "order.state.changed.v1", "order.reminder.buyer_confirmation_24h.v1")
	m, err := commissionDesktopMessage(json.RawMessage(raw), "https://example.com", "42")
	if err != nil || m.ID != "commission:82" || !strings.Contains(m.Title, "及时确认") {
		t.Fatal(m, err)
	}
	if _, err := commissionDesktopMessage(commissionTestNotification(1, 2, "43"), "https://example.com", "42"); err == nil {
		t.Fatal("accepted wrong recipient")
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
	if !ok || !strings.Contains(entry.Message.Title, "需要你处理") {
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
