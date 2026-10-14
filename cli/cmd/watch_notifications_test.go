package cmd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/desktopqueue"
	"cli.eigenflux.ai/internal/dispatch"
)

func newNotificationWatch(t *testing.T, endpoint string) *accountWatch {
	t.Helper()
	w := newCommissionWatch(t, endpoint, "commission_order")
	if err := os.Remove(dispatch.BindingPath(w.home, w.server.Name)); err != nil {
		t.Fatal(err)
	}
	w.binding, w.journal, w.runAgent = nil, nil, nil
	w.notificationsOnly = true
	if err := w.enableNotificationsOnly(); err != nil {
		t.Fatal(err)
	}
	if err := w.enableDesktop(); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestNotificationsOnlyPersistsBeforeACKAndLegacyStreamDoesNotConsume(t *testing.T) {
	var w *accountWatch
	var ack atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/notifications/ack" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.NotFound(out, r)
			return
		}
		q, err := desktopqueue.Open(filepath.Join(w.home, "watch", "desktop-"+w.scope+".json"), w.scope)
		if err != nil {
			t.Error(err)
		} else if pending, _ := q.Counts(); pending != 1 {
			t.Errorf("ACK before persistence: %d", pending)
		}
		ack.Add(1)
		_, _ = io.WriteString(out, `{"code":0,"data":{}}`)
	}))
	defer server.Close()
	w = newNotificationWatch(t, server.URL)
	if w.receivesPM() || !w.receivesCommission() || w.journal != nil || w.runAgent != nil {
		t.Fatal("notification mode enabled execution or PM")
	}
	page := commissionTestPage(commissionTestNotification(81, 901, "42"))
	for i := 0; i < 2; i++ {
		if err := w.deliverCommissionNotifications(context.Background(), page, "http_poll"); err != nil {
			t.Fatal(err)
		}
	}
	if ack.Load() != 2 {
		t.Fatal(ack.Load())
	}
	if pending, _ := w.desktop.Counts(); pending != 1 {
		t.Fatal("duplicate queue entry")
	}
	api := client.New(server.URL+"/api/v2", "token", "test", client.Meta{})
	if owned, err := orderNotificationsOwnedByWatch(api); err != nil || !owned {
		t.Fatalf("legacy ownership: %v %v", owned, err)
	}
	if err := drainOrderNotifications(api, "json", "en", io.Discard); err != nil {
		t.Fatal(err)
	}
	if ack.Load() != 2 {
		t.Fatal("legacy stream consumed notification")
	}
}

func TestNotificationsOnlyNeverACKsWrongRecipientOrUnavailableQueue(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(out, `{"code":0,"data":{}}`)
	}))
	defer server.Close()
	w := newNotificationWatch(t, server.URL)
	if err := w.deliverCommissionNotifications(context.Background(), commissionTestPage(commissionTestNotification(81, 901, "43")), "http_poll"); err == nil {
		t.Fatal("wrong account accepted")
	}
	w.desktop = nil
	if err := w.deliverCommissionNotifications(context.Background(), commissionTestPage(commissionTestNotification(81, 901, "42")), "http_poll"); err == nil {
		t.Fatal("missing queue acknowledged")
	}
	if calls.Load() != 0 {
		t.Fatal("ACKed unsafe notification")
	}
}

func TestNotificationsOnlyPinsIdentityAndPreservesExecutionBinding(t *testing.T) {
	w := newCommissionWatch(t, "https://example.com", "commission_order")
	w.notificationsOnly = true
	if err := w.enableNotificationsOnly(); err == nil {
		t.Fatal("replaced execution ownership")
	}
	if err := os.Remove(dispatch.BindingPath(w.home, w.server.Name)); err != nil {
		t.Fatal(err)
	}
	w.binding, w.journal = nil, nil
	if err := w.enableNotificationsOnly(); err != nil {
		t.Fatal(err)
	}
	credentials := w.identity
	credentials.AgentID = "43"
	if err := auth.SaveV2Credentials(w.server.Name, &credentials); err != nil {
		t.Fatal(err)
	}
	if err := w.notificationIdentityOK(); !errors.Is(err, errWatchIdentity) {
		t.Fatalf("did not stop on account switch: %v", err)
	}
	w.identity = credentials
	if err := w.enableNotificationsOnly(); !errors.Is(err, errWatchIdentity) {
		t.Fatal("retargeted previous notification owner", err)
	}
}

func TestNotificationsOnlyPollReadinessDoesNotUsePMOrAgentRuntime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/notifications/pending" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.NotFound(out, r)
			return
		}
		_, _ = io.WriteString(out, `{"code":0,"data":{"notifications":[],"has_more":false}}`)
	}))
	defer server.Close()
	w := newNotificationWatch(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := false
	w.emit = func(kind string, _ interface{}) error {
		if kind == "notifications_ready" {
			ready = true
			cancel()
		}
		return nil
	}
	if err := w.commissionFallbackLoop(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !ready || w.runtimeOnline.Load() || w.wsOnline.Load() {
		t.Fatal("incorrect readiness")
	}
}
