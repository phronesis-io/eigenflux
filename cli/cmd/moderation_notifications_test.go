package cmd

import (
	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/desktopqueue"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func moderationTestNotification(recipient, status string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"notification_id":"81","source_type":"commission_moderation","type":"commission.moderation.changed.v1","payload":{"moderation_id":"9007199254740995","commission_id":"9007199254740993","recipient_agent_id":"%s","draft_version":3,"notification_version":1,"status":"%s","decision":"review","field":"delivery_spec_text","reason":"请补充交付范围","occurred_at":1700000000000}}`, recipient, status))
}
func TestModerationWatchPersistsBeforeACKWithoutExecution(t *testing.T) {
	var watch *accountWatch
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/notifications/ack" {
			t.Errorf("unexpected path %s", r.URL.Path)
			out.WriteHeader(404)
			return
		}
		var body struct {
			Notifications []struct {
				Source string `json:"source_type"`
			} `json:"notifications"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Notifications) != 1 || body.Notifications[0].Source != moderationNotificationSource {
			t.Error("wrong ACK source")
		}
		queue, err := desktopqueue.Open(filepath.Join(watch.home, "watch", "desktop-"+watch.scope+".json"), watch.scope)
		if err != nil {
			t.Error(err)
		} else if pending, _ := queue.Counts(); pending != 1 {
			t.Error("ACK before durable save")
		}
		calls.Add(1)
		io.WriteString(out, `{"code":0,"data":{}}`)
	}))
	defer server.Close()
	watch = newCommissionWatch(t, server.URL, "commission_order")
	if err := watch.enableDesktop(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := watch.deliverCommissionNotifications(context.Background(), commissionTestPage(moderationTestNotification("42", "review")), "http_poll"); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
	if pending, _ := watch.desktop.Counts(); pending != 1 {
		t.Fatal("duplicate notification")
	}
	if len(watch.journal.Snapshot()) != 0 || watch.mainNotices != nil {
		t.Fatal("moderation event entered Agent execution")
	}
}
func TestModerationWatchRetainsInvalidOrUndeliverableNotices(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(out, `{"code":0,"data":{}}`)
	}))
	defer server.Close()
	watch := newNotificationWatch(t, server.URL)
	for _, raw := range []json.RawMessage{moderationTestNotification("43", "reject"), moderationTestNotification("42", "invalid"), json.RawMessage(strings.Replace(string(moderationTestNotification("42", "review")), `"notification_version":1`, `"notification_version":0`, 1))} {
		if err := watch.deliverCommissionNotifications(context.Background(), commissionTestPage(raw), "socket"); err == nil {
			t.Fatal("invalid notice accepted")
		}
	}
	watch.desktop = nil
	if err := watch.deliverCommissionNotifications(context.Background(), commissionTestPage(moderationTestNotification("42", "published")), "http_poll"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("undeliverable notice acknowledged")
	}
}
func TestModerationNoticeTextAndStringIDs(t *testing.T) {
	for _, status := range []string{"pending", "review", "reject", "pass", "published", "failed", "stale"} {
		notice, err := parseModerationNotification(moderationTestNotification("42", status), "42")
		if err != nil {
			t.Fatal(err)
		}
		for _, language := range []string{"en", "zh"} {
			title, body := moderationNotificationText(language, notice)
			if !strings.Contains(title, "9007199254740993") || !strings.Contains(body, "请补充交付范围") {
				t.Fatal(title, body)
			}
		}
		var output strings.Builder
		if err := renderModerationNotification(&output, "json", "zh", notice); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), `"commission_id":"9007199254740993"`) {
			t.Fatal(output.String())
		}
	}
	if moderationStatusText("zh", "pass") == moderationStatusText("zh", "published") {
		t.Fatal("approval claims publication")
	}
}
func TestDrainModerationNotificationsRendersBeforeACK(t *testing.T) {
	var output strings.Builder
	var agentID string
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/notifications/pending":
			fmt.Fprintf(out, `{"code":0,"data":%s}`, commissionTestPage(moderationTestNotification(agentID, "reject")))
		case "/api/v2/notifications/ack":
			if !strings.Contains(output.String(), "审核未通过") {
				t.Error("ACK before render")
			}
			calls++
			io.WriteString(out, `{"code":0,"data":{}}`)
		default:
			out.WriteHeader(404)
		}
	}))
	defer server.Close()
	_, name := runtimeTestConfig(t, server.URL, true)
	if _, _, _, err := auth.LoadOrCreateIdentity(name); err != nil {
		t.Fatal(err)
	}
	creds, err := auth.LoadV2Credentials(name)
	if err != nil {
		t.Fatal(err)
	}
	creds.AgentID = "42"
	if err := auth.SaveV2Credentials(name, creds); err != nil {
		t.Fatal(err)
	}
	agentID = creds.AgentID
	api := client.New(server.URL+"/api/v2", "token", "test", client.Meta{})
	if err := drainOrderNotifications(api, "table", "zh", &output); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
