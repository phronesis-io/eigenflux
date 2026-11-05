package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/desktopqueue"
	"cli.eigenflux.ai/internal/dispatch"
	"github.com/gorilla/websocket"
)

func TestGenericNotificationSocketPreservesLegacyDataWithoutHTTP(t *testing.T) {
	for _, mode := range []string{"plain", "pm", "orders"} {
		t.Run(mode, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) { requests++; out.WriteHeader(503) }))
			defer server.Close()
			watch := newCommissionWatch(t, server.URL, "pm_push")
			if mode == "plain" {
				watch.binding, watch.journal = nil, nil
			}
			if mode == "orders" {
				watch.binding.Events = []string{"commission_order"}
			}
			credentials := watch.identity
			credentials.ExpiresAt = time.Now().Add(-time.Hour).UnixMilli()
			if err := auth.SaveV2Credentials(watch.server.Name, &credentials); err != nil {
				t.Fatal(err)
			}
			var output json.RawMessage
			watch.emit = func(kind string, value interface{}) error {
				if kind != "notification_push" {
					t.Fatal(kind)
				}
				output = value.(json.RawMessage)
				return nil
			}
			notifications := []json.RawMessage{json.RawMessage(`{"source_type":"friend_request","unknown":{"id":9007199254740993}}`), json.RawMessage(`{"source_type":"system","content":"hello"}`), json.RawMessage(`{"source_type":"milestone","achievement":"first"}`), json.RawMessage(`{"future_field":true}`)}
			if mode != "orders" {
				notifications = append(notifications, commissionTestNotification(88, 901, "42"))
			}
			raw, _ := json.Marshal(map[string]any{"notifications": notifications, "unknown_envelope": json.RawMessage(`9007199254740993`), "next_cursor": "legacy-cursor"})
			if err := watch.deliverSocketEvent(context.Background(), "notification_push", raw); err != nil {
				t.Fatal(err)
			}
			if string(output) != string(raw) || requests != 0 {
				t.Fatal("generic output changed or attempted HTTP", string(output), requests)
			}
		})
	}
}

func TestMixedNotificationSocketOnlyRemovesModeration(t *testing.T) {
	for _, subscribed := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "orders"}[subscribed], func(t *testing.T) {
			var acks []map[string]string
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				var body struct {
					Notifications []map[string]string `json:"notifications"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				acks = append(acks, body.Notifications...)
				io.WriteString(out, `{"code":0,"data":{}}`)
			}))
			defer server.Close()
			events := []string{"pm_push"}
			if subscribed {
				events = append(events, "commission_order")
			}
			watch := newCommissionWatch(t, server.URL, events...)
			generic := []json.RawMessage{json.RawMessage(`{"source_type":"friend_request","custom":9007199254740993}`), json.RawMessage(`{"source_type":"system"}`), json.RawMessage(`{"source_type":"milestone"}`), commissionTestNotification(82, 901, "42"), json.RawMessage(`{"unknown_source":true}`)}
			data := append(append([]json.RawMessage{}, generic...), moderationTestNotification("42", "review"))
			raw, _ := json.Marshal(map[string]any{"notifications": data, "has_more": true, "next_cursor": "opaque", "unknown": json.RawMessage(`{"n":9007199254740993}`)})
			dedicated, forwarded := 0, 0
			watch.emit = func(kind string, value interface{}) error {
				if kind == "commission_moderation_notification" {
					dedicated++
					return nil
				}
				if kind != "notification_push" {
					t.Fatal(kind)
				}
				forwarded++
				encoded, _ := json.Marshal(value)
				var envelope map[string]json.RawMessage
				_ = json.Unmarshal(encoded, &envelope)
				want, _ := json.Marshal(generic)
				if string(envelope["notifications"]) != string(want) || string(envelope["unknown"]) != `{"n":9007199254740993}` || string(envelope["next_cursor"]) != `"opaque"` {
					t.Fatal("generic contract changed", string(encoded))
				}
				return nil
			}
			if err := watch.deliverSocketEvent(context.Background(), "notification_push", raw); err != nil {
				t.Fatal(err)
			}
			expected := 1
			if subscribed {
				expected = 2
			}
			if dedicated != 1 || forwarded != 1 || len(acks) != expected {
				t.Fatal(dedicated, forwarded, acks)
			}
			if !subscribed && len(watch.journal.Snapshot()) != 0 {
				t.Fatal("unsubscribed Order executed")
			}
		})
	}
}

func TestModerationDesktopFailureDoesNotCompleteReceiptOrACK(t *testing.T) {
	var watch *accountWatch
	acks, events := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		acks++
		queue, err := desktopqueue.Open(filepath.Join(watch.home, "watch", "desktop-"+watch.scope+".json"), watch.scope)
		if err != nil {
			t.Error(err)
		} else if pending, _ := queue.Counts(); pending != 1 {
			t.Error("ACK before desktop persistence")
		}
		io.WriteString(out, `{"code":0,"data":{}}`)
	}))
	defer server.Close()
	watch = newCommissionWatch(t, server.URL, "pm_push")
	if err := watch.enableDesktop(); err != nil {
		t.Fatal(err)
	}
	watch.emit = func(string, interface{}) error { events++; return nil }
	path := filepath.Join(watch.home, "watch", "desktop-"+watch.scope+".json")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	page := commissionTestPage(moderationTestNotification("42", "review"))
	if err := watch.deliverCommissionNotifications(context.Background(), page, "socket"); err == nil {
		t.Fatal("desktop persistence failure ignored")
	}
	inbox, err := readModerationInbox(watch.home, watch.scope, "42")
	if err != nil || len(inbox.Receipts) != 0 || acks != 0 || events != 0 {
		t.Fatal("completed prematurely", err, inbox, acks, events)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := watch.deliverCommissionNotifications(context.Background(), page, "socket"); err != nil {
			t.Fatal(err)
		}
	}
	if acks != 2 || events != 1 {
		t.Fatal(acks, events)
	}
}

func TestModerationInboxFailureAfterDesktopEnqueueDeduplicatesRetry(t *testing.T) {
	watch := newCommissionWatch(t, "https://eigenflux.ai", "pm_push")
	if err := watch.enableDesktop(); err != nil {
		t.Fatal(err)
	}
	notice, _ := parseModerationNotification(moderationTestNotification("42", "review"), "42")
	path := moderationInboxPath(watch.home, watch.scope)
	// The sink succeeded but the completed receipt write then failed.
	_, err := saveModerationNoticeWithSink(watch.home, watch.scope, "42", notice, func() error {
		message, err := commissionDesktopMessage(commissionTestNotification(82, 901, "42"), watch.server.Endpoint, "42", "en")
		if err != nil {
			return err
		}
		message.ID = watch.scope + ":moderation:81"
		if err := watch.desktop.Enqueue(message); err != nil {
			return err
		}
		return os.Mkdir(path, 0700)
	})
	if err == nil {
		t.Fatal("inbox write should fail")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := watch.enqueueModerationNotification(notice); err != nil {
		t.Fatal(err)
	}
	if pending, _ := watch.desktop.Counts(); pending != 1 {
		t.Fatal("desktop retry duplicated", pending)
	}
	inbox, err := readModerationInbox(watch.home, watch.scope, "42")
	if err != nil || len(inbox.Receipts) != 1 {
		t.Fatal("receipt not recovered", err)
	}
}

func TestModerationReconciliationUsesPMFallbackCadence(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(out, `{"code":0,"data":{"notifications":[],"has_more":false}}`)
	}))
	defer server.Close()
	watch := newCommissionWatch(t, server.URL, "pm_push")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := watch.commissionFallbackLoop(ctx); err != nil || calls != 0 {
		t.Fatal("ordinary watch started extra poll loop", err, calls)
	}
	last := time.Now()
	watch.wsOnline.Store(true)
	if next, err := watch.reconcileModeration(ctx, last, last.Add(time.Minute)); err != nil || !next.Equal(last) || calls != 0 {
		t.Fatal(next, err, calls)
	}
	next, err := watch.reconcileModeration(ctx, last, last.Add(moderationReconcileInterval))
	if err != nil || calls != 1 {
		t.Fatal(next, err, calls)
	}
	watch.wsOnline.Store(false)
	if _, err := watch.reconcileModeration(ctx, next, next.Add(time.Minute)); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
	watch.binding.Events = append(watch.binding.Events, "commission_order")
	if err := dispatch.WriteJSON(dispatch.BindingPath(watch.home, watch.server.Name), watch.binding); err != nil {
		t.Fatal(err)
	}
	if _, err := watch.reconcileModeration(ctx, next, next.Add(time.Hour)); err != nil || calls != 2 {
		t.Fatal("duplicate Order fallback", err, calls)
	}
}

func TestModerationSocketMetadataCannotLeakDedicatedNotice(t *testing.T) {
	watch := newCommissionWatch(t, "https://eigenflux.ai", "pm_push")
	emitted := false
	watch.emit = func(string, interface{}) error { emitted = true; return nil }
	raw := json.RawMessage(`{"has_more":"wrong","notifications":[` + string(moderationTestNotification("42", "review")) + `]}`)
	if err := watch.deliverSocketEvent(context.Background(), "notification_push", raw); err == nil || emitted {
		t.Fatal("malformed moderation envelope forwarded", err)
	}
}

func TestModerationPMOnlySocketReconnectReplaysWithoutHTTPPoll(t *testing.T) {
	var sockets, polls atomic.Int32
	acked := make(chan struct{}, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/agent/events/ws":
			conn, err := upgrader.Upgrade(out, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			if sockets.Add(1) == 1 {
				return
			}
			_ = conn.WriteJSON(map[string]any{"type": "notification_push", "data": commissionTestPage(moderationTestNotification("42", "review"))})
			_, _, _ = conn.ReadMessage()
		case "/api/v2/notifications/ack":
			io.WriteString(out, `{"code":0,"data":{}}`)
			select {
			case acked <- struct{}{}:
			default:
			}
		default:
			polls.Add(1)
			out.WriteHeader(500)
		}
	}))
	defer server.Close()
	watch := newCommissionWatch(t, server.URL, "pm_push")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := watch.commissionFallbackLoop(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- watch.pmLoop(ctx) }()
	select {
	case <-acked:
	case <-ctx.Done():
		t.Fatal("reconnected socket did not deliver moderation")
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	inbox, err := readModerationInbox(watch.home, watch.scope, "42")
	if err != nil || len(inbox.Notices) != 1 || len(watch.journal.Snapshot()) != 0 || sockets.Load() < 2 || polls.Load() != 0 {
		t.Fatal("replay/ownership failed", err, sockets.Load(), polls.Load())
	}
}
