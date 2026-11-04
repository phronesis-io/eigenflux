package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"github.com/gorilla/websocket"
)

func TestModerationOrdinaryPollDoesNotReplayGenericNotifications(t *testing.T) {
	for _, plain := range []bool{false, true} {
		t.Run(strconv.FormatBool(plain), func(t *testing.T) {
			var acks []map[string]string
			generic := []json.RawMessage{json.RawMessage(`{"source_type":"friend_request"}`), json.RawMessage(`{"source_type":"system"}`), json.RawMessage(`{"source_type":"milestone"}`), commissionTestNotification(82, 901, "42")}
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/notifications/pending" {
					fmt.Fprintf(out, `{"code":0,"data":%s}`, commissionTestPage(append(generic, moderationTestNotification("42", "review"))...))
					return
				}
				var body struct {
					Notifications []map[string]string `json:"notifications"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				acks = append(acks, body.Notifications...)
				io.WriteString(out, `{"code":0,"data":{}}`)
			}))
			defer server.Close()
			watch := newCommissionWatch(t, server.URL, "pm_push")
			if plain {
				watch.binding, watch.journal = nil, nil
			}
			dedicated, forwarded := 0, 0
			watch.emit = func(kind string, _ interface{}) error {
				if kind == "commission_moderation_notification" {
					dedicated++
				}
				if kind == "notification_push" {
					forwarded++
				}
				return nil
			}
			for i := 0; i < 3; i++ {
				if err := watch.pollCommissionNotifications(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if dedicated != 1 || forwarded != 0 || len(acks) != 3 {
				t.Fatal(dedicated, forwarded, acks)
			}
			for _, ack := range acks {
				if ack["source_type"] != moderationNotificationSource {
					t.Fatal(ack)
				}
			}
			if err := watch.deliverSocketEvent(context.Background(), "notification_push", commissionTestPage(generic...)); err != nil || forwarded != 1 {
				t.Fatal("socket generic contract changed", err, forwarded)
			}
		})
	}
}

func TestNotificationsOnlyCountUsesAcknowledgedOwnedOrders(t *testing.T) {
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
	watch := newNotificationWatch(t, server.URL)
	counts := []int{}
	watch.emit = func(kind string, value interface{}) error {
		if kind == "notification_push" {
			counts = append(counts, value.(map[string]interface{})["count"].(int))
		}
		return nil
	}
	coordination := json.RawMessage(strings.ReplaceAll(string(commissionTestNotification(83, 902, "42")), "order.state.changed.v1", "order.inputs.requested.v1"))
	generic := json.RawMessage(`{"source_type":"system","notification_id":"84"}`)
	if err := watch.deliverCommissionNotifications(context.Background(), commissionTestPage(commissionTestNotification(82, 901, "42"), coordination, generic, moderationTestNotification("42", "review")), "http_poll"); err != nil {
		t.Fatal(err)
	}
	if len(counts) != 1 || counts[0] != 1 || len(acks) != 2 {
		t.Fatal(counts, acks)
	}
	for _, ack := range acks {
		if ack["notification_id"] == "83" || ack["notification_id"] == "84" {
			t.Fatal("unhandled row acknowledged", ack)
		}
	}
	if err := watch.deliverCommissionNotifications(context.Background(), commissionTestPage(coordination, generic), "http_poll"); err != nil || len(counts) != 1 {
		t.Fatal("empty count event emitted", err, counts)
	}
}

func TestModerationSinkFailureIsolatedAtEitherPositionAndRecovers(t *testing.T) {
	for _, failedFirst := range []bool{true, false} {
		t.Run(strconv.FormatBool(failedFirst), func(t *testing.T) {
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
			watch := newCommissionWatch(t, server.URL, "commission_order")
			path := moderationInboxPath(watch.home, watch.scope)
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			forwarded := 0
			watch.emit = func(kind string, value interface{}) error {
				if kind == "notification_push" {
					forwarded++
					raw, _ := json.Marshal(value)
					if strings.Contains(string(raw), moderationNotificationSource) {
						t.Fatal("moderation leaked")
					}
				}
				return nil
			}
			rows := []json.RawMessage{moderationTestNotification("42", "review"), commissionTestNotification(82, 901, "42")}
			if !failedFirst {
				rows[0], rows[1] = rows[1], rows[0]
			}
			rows = append(rows, json.RawMessage(`{"source_type":"system"}`))
			err := watch.deliverCommissionNotifications(context.Background(), commissionTestPage(rows...), "socket")
			var partial *moderationPageError
			if !errors.As(err, &partial) || len(acks) != 1 || acks[0]["notification_id"] != "82" || forwarded != 1 {
				t.Fatal("neighbor blocked/lost", err, acks, forwarded)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := watch.deliverCommissionNotifications(context.Background(), commissionTestPage(moderationTestNotification("42", "review")), "http_poll"); err != nil || len(acks) != 2 || acks[1]["notification_id"] != "81" {
				t.Fatal("failed row not recoverable", err, acks)
			}
		})
	}
}

func TestModerationFailedSinkDoesNotBlockCompletedReceipt(t *testing.T) {
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
	watch := newCommissionWatch(t, server.URL, "pm_push")
	completed, _ := parseModerationNotification(moderationTestNotification("42", "published"), "42")
	completed.ID = 82
	if _, err := saveModerationNotice(watch.home, watch.scope, "42", completed); err != nil {
		t.Fatal(err)
	}
	if err := watch.enableDesktop(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(watch.home, "watch", "desktop-"+watch.scope+".json"), 0700); err != nil {
		t.Fatal(err)
	}
	completedRaw, _ := json.Marshal(completed)
	err := watch.deliverCommissionNotifications(context.Background(), commissionTestPage(moderationTestNotification("42", "review"), completedRaw), "http_poll")
	if err == nil || len(acks) != 1 || acks[0]["notification_id"] != "82" {
		t.Fatal("completed independent sink blocked", err, acks)
	}
}

func TestModerationPartialFailureContinuesCursorPagesAndReportsCap(t *testing.T) {
	for _, capWalk := range []bool{false, true} {
		t.Run(strconv.FormatBool(capWalk), func(t *testing.T) {
			pages, acks := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/notifications/ack" {
					acks++
					io.WriteString(out, `{"code":0,"data":{}}`)
					return
				}
				pages++
				page := commissionNotificationPage{Notifications: []json.RawMessage{moderationTestNotification("42", "review")}, HasMore: true, NextCursor: strconv.Itoa(pages)}
				if pages == 2 && !capWalk {
					page = commissionNotificationPage{Notifications: []json.RawMessage{commissionTestNotification(82, 901, "42")}}
				}
				raw, _ := json.Marshal(page)
				fmt.Fprintf(out, `{"code":0,"data":%s}`, raw)
			}))
			defer server.Close()
			watch := newCommissionWatch(t, server.URL, "commission_order")
			if err := os.Mkdir(moderationInboxPath(watch.home, watch.scope), 0700); err != nil {
				t.Fatal(err)
			}
			err := watch.pollCommissionNotifications(context.Background())
			var sink *moderationSinkError
			if !errors.As(err, &sink) {
				t.Fatal("failure hidden", err)
			}
			if capWalk && (pages != commissionPollPages || acks != 0) || !capWalk && (pages != 2 || acks != 1) {
				t.Fatal("cursor walk starved/stopped", pages, acks)
			}
		})
	}
}

func TestModerationPartialFailureNeverMasksFatalErrors(t *testing.T) {
	for _, mode := range []string{"ack", "output", "identity", "cancelled", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			pages := 0
			fatal := errors.New("output failed")
			switch mode {
			case "identity":
				fatal = errWatchIdentity
			case "cancelled":
				fatal = context.Canceled
			case "deadline":
				fatal = context.DeadlineExceeded
			}
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/notifications/ack" {
					if mode == "ack" {
						io.WriteString(out, `{"code":500,"data":{}}`)
					} else {
						io.WriteString(out, `{"code":0,"data":{}}`)
					}
					return
				}
				pages++
				page := commissionNotificationPage{Notifications: []json.RawMessage{moderationTestNotification("42", "review"), commissionTestNotification(82, 901, "42")}, HasMore: true, NextCursor: "next"}
				raw, _ := json.Marshal(page)
				fmt.Fprintf(out, `{"code":0,"data":%s}`, raw)
			}))
			defer server.Close()
			watch := newCommissionWatch(t, server.URL, "commission_order")
			if err := os.Mkdir(moderationInboxPath(watch.home, watch.scope), 0700); err != nil {
				t.Fatal(err)
			}
			watch.emit = func(kind string, _ interface{}) error {
				if kind == "notification_push" {
					return fatal
				}
				return nil
			}
			err := watch.pollCommissionNotifications(context.Background())
			var partial *moderationPageError
			if err == nil || errors.As(err, &partial) || pages != 1 || mode != "ack" && !errors.Is(err, fatal) {
				t.Fatal("fatal became recoverable", err, pages)
			}
		})
	}
}

func TestModerationIdentityChangeBeforeACKStopsWholeBatch(t *testing.T) {
	acks := 0
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) { acks++; io.WriteString(out, `{"code":0,"data":{}}`) }))
	defer server.Close()
	watch := newCommissionWatch(t, server.URL, "commission_order")
	watch.emit = func(kind string, _ interface{}) error {
		if kind == "commission_notification_invalid" {
			credentials := watch.identity
			credentials.AgentID = "43"
			return auth.SaveV2Credentials(watch.server.Name, &credentials)
		}
		return nil
	}
	err := watch.deliverCommissionNotifications(context.Background(), commissionTestPage(commissionTestNotification(82, 901, "42"), moderationTestNotification("43", "review")), "socket")
	if !errors.Is(err, errWatchIdentity) || acks != 0 {
		t.Fatal("new identity acknowledged old rows", err, acks)
	}
}

func TestModerationSoftSocketErrorKeepsFollowingPMFrame(t *testing.T) {
	var sockets, acks atomic.Int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/notifications/ack" {
			acks.Add(1)
			io.WriteString(out, `{"code":0,"data":{}}`)
			return
		}
		conn, err := upgrader.Upgrade(out, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		sockets.Add(1)
		_ = conn.WriteJSON(map[string]any{"type": "notification_push", "data": commissionTestPage(moderationTestNotification("42", "review"), commissionTestNotification(82, 901, "42"))})
		_ = conn.WriteJSON(map[string]any{"type": "pm_push", "data": json.RawMessage(`{"messages":[],"next_cursor":"pm-next"}`)})
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	watch := newCommissionWatch(t, server.URL, "pm_push", "commission_order")
	if err := os.Mkdir(moderationInboxPath(watch.home, watch.scope), 0700); err != nil {
		t.Fatal(err)
	}
	pm := make(chan struct{}, 1)
	var diagnostics atomic.Int32
	watch.emit = func(kind string, value interface{}) error {
		if kind == "diagnostic" {
			fields := value.(map[string]string)
			if fields["area"] == "moderation_delivery" {
				if fields["code"] != "moderation_sink_failed" {
					t.Error(fields)
				}
				diagnostics.Add(1)
			}
		}
		if kind == "pm_push" {
			select {
			case pm <- struct{}{}:
			default:
			}
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- watch.pmLoop(ctx) }()
	select {
	case <-pm:
	case <-ctx.Done():
		t.Fatal("soft row failure starved PM frame")
	}
	cancel()
	<-done
	if sockets.Load() != 1 || acks.Load() != 1 || diagnostics.Load() != 1 {
		t.Fatal(sockets.Load(), acks.Load(), diagnostics.Load())
	}
	outputErr := errors.New("diagnostic output closed")
	watch.emit = func(kind string, _ interface{}) error {
		if kind == "diagnostic" {
			return outputErr
		}
		return nil
	}
	if err := watch.deliverSocketEvent(context.Background(), "notification_push", commissionTestPage(moderationTestNotification("42", "review"))); !errors.Is(err, outputErr) {
		t.Fatal("diagnostic output failure swallowed", err)
	}
}
