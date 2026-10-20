package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"cli.eigenflux.ai/internal/desktopqueue"
	"cli.eigenflux.ai/internal/dispatch"
)

func inputCoordinationNotice(event string) json.RawMessage {
	raw := strings.ReplaceAll(string(commissionTestNotification(81, 901, "42")), "order.state.changed.v1", event)
	return json.RawMessage(strings.ReplaceAll(raw, "in_progress", "awaiting_seller"))
}

func TestCommissionInputCoordinationIsDurableAndSilent(t *testing.T) {
	for _, event := range []string{"order.inputs.requested.v1", "order.inputs.supplied.v1"} {
		t.Run(event, func(t *testing.T) {
			var watch *accountWatch
			var ack atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2/notifications/ack" {
					t.Errorf("unexpected request %s", r.URL.Path)
				}
				journal, err := dispatch.OpenJournal(*watch.binding)
				if err != nil || len(journal.Snapshot()) != 1 {
					t.Error("coordination ACK before durable intake")
				}
				ack.Add(1)
				_, _ = io.WriteString(out, `{"code":0,"data":{}}`)
			}))
			defer server.Close()
			watch = newCommissionWatch(t, server.URL, "commission_order")
			var err error
			watch.desktop, err = desktopqueue.Open(filepath.Join(t.TempDir(), "desktop.json"), watch.scope)
			if err != nil {
				t.Fatal(err)
			}
			for _, transport := range []string{"socket", "http_poll"} {
				if err := watch.deliverCommissionNotifications(context.Background(), commissionTestPage(inputCoordinationNotice(event)), transport); err != nil {
					t.Fatal(err)
				}
			}
			pending, delivered := watch.desktop.Counts()
			if ack.Load() != 2 || len(watch.journal.Snapshot()) != 1 || pending != 0 || delivered != 0 || watch.mainNotices != nil {
				t.Fatalf("coordination was lost or surfaced: ack=%d desktop=%d/%d", ack.Load(), pending, delivered)
			}
		})
	}
}

func TestNotificationsOnlyLeavesAgentCoordinationPending(t *testing.T) {
	var ack atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
		ack.Add(1)
		_, _ = io.WriteString(out, `{"code":0,"data":{}}`)
	}))
	defer server.Close()
	watch := newNotificationWatch(t, server.URL)
	if err := watch.deliverCommissionNotifications(context.Background(), commissionTestPage(inputCoordinationNotice("order.inputs.requested.v1")), "http_poll"); err != nil {
		t.Fatal(err)
	}
	if ack.Load() != 0 || watch.mainNotices != nil {
		t.Fatal("notifications-only consumer swallowed Agent coordination")
	}
}
