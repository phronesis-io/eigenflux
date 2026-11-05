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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/desktopqueue"
	"cli.eigenflux.ai/internal/dispatch"
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
func TestModerationWatchRetainsInvalidNoticesAndStoresWithoutDesktop(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(out, `{"code":0,"data":{}}`)
	}))
	defer server.Close()
	watch := newNotificationWatch(t, server.URL)
	for _, raw := range []json.RawMessage{moderationTestNotification("43", "reject"), moderationTestNotification("42", "invalid"), json.RawMessage(strings.Replace(string(moderationTestNotification("42", "review")), `"notification_version":1`, `"notification_version":0`, 1))} {
		if err := watch.deliverCommissionNotifications(context.Background(), commissionTestPage(raw), "socket"); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid notice acknowledged")
	}
	watch.desktop = nil
	if err := watch.deliverCommissionNotifications(context.Background(), commissionTestPage(moderationTestNotification("42", "published")), "http_poll"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("durable fallback not acknowledged")
	}
	inbox, err := readModerationInbox(watch.home, watch.scope, watch.identity.AgentID)
	if err != nil || len(inbox.Notices) != 1 {
		t.Fatal("missing durable fallback", err)
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
	if got := moderationStatusText("zh", "pass"); got != "审核通过，等待上架" {
		t.Fatal("approval must not imply manual review", got)
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
	if err := drainCommissionNotifications(api, "table", "zh", &output); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}

func TestModerationFirstSellerWithoutOrderSubscriptionOrDesktop(t *testing.T) {
	var watch *accountWatch
	var acks []map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/notifications/pending" {
			fmt.Fprintf(out, `{"code":0,"data":%s}`, commissionTestPage(moderationTestNotification("42", "review"), commissionTestNotification(82, 901, "42")))
			return
		}
		var body struct {
			Notifications []map[string]string `json:"notifications"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		acks = append(acks, body.Notifications...)
		inbox, err := readModerationInbox(watch.home, watch.scope, "42")
		if err != nil || len(inbox.Notices) != 1 {
			t.Error("ACK before durable inbox", err)
		}
		io.WriteString(out, `{"code":0,"data":{}}`)
	}))
	defer server.Close()
	watch = newCommissionWatch(t, server.URL, "pm_push")
	events := 0
	watch.emit = func(kind string, data interface{}) error {
		if kind == "notification_push" {
			raw, _ := json.Marshal(data)
			if strings.Contains(string(raw), moderationNotificationSource) {
				t.Fatal("moderation reached host Order route")
			}
			return nil
		}
		if kind != "commission_moderation_notification" {
			t.Fatalf("unexpected event %s", kind)
		}
		raw, _ := json.Marshal(data)
		if strings.Contains(string(raw), `"order_id"`) || strings.Contains(string(raw), `"messages"`) || strings.Contains(string(raw), `"next_cursor"`) {
			t.Fatal("actionable host envelope", string(raw))
		}
		events++
		return nil
	}
	for i := 0; i < 2; i++ {
		if err := watch.pollCommissionNotifications(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if events != 1 || len(acks) != 2 || len(watch.journal.Snapshot()) != 0 || watch.mainNotices != nil {
		t.Fatal("dedupe or execution boundary", events, acks)
	}
	for _, ack := range acks {
		if ack["source_type"] != moderationNotificationSource {
			t.Fatal("unsubscribed Order acknowledged")
		}
	}
}
func TestMalformedModerationDoesNotPreventOrderACK(t *testing.T) {
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
	var orders int
	watch.emit = func(kind string, data interface{}) error {
		if kind == "notification_push" {
			raw, _ := json.Marshal(data)
			if strings.Contains(string(raw), moderationNotificationSource) {
				t.Fatal("moderation leaked into host order route")
			}
			orders++
		}
		return nil
	}
	for _, page := range []json.RawMessage{commissionTestPage(moderationTestNotification("43", "review"), commissionTestNotification(82, 901, "42")), commissionTestPage(commissionTestNotification(82, 901, "42"), moderationTestNotification("42", "invalid"))} {
		if err := watch.deliverCommissionNotifications(context.Background(), page, "socket"); err != nil {
			t.Fatal(err)
		}
	}
	if len(acks) != 2 || orders != 2 {
		t.Fatal(acks, orders)
	}
	for _, ack := range acks {
		if ack["notification_id"] != "82" || ack["source_type"] != "commission_order" {
			t.Fatal(ack)
		}
	}
}
func TestModerationInboxRetentionAndCorruption(t *testing.T) {
	home := t.TempDir()
	notice, err := parseModerationNotification(moderationTestNotification("42", "review"), "42")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= moderationInboxLimit+1; i++ {
		notice.ID = notificationID(i)
		if _, err := saveModerationNotice(home, "scope", "42", notice); err != nil {
			t.Fatal(err)
		}
	}
	inbox, err := readModerationInbox(home, "scope", "42")
	if err != nil || len(inbox.Notices) != moderationInboxLimit || inbox.Notices[0].ID != 2 {
		t.Fatal(inbox, err)
	}
	if fresh, err := saveModerationNotice(home, "scope", "42", notice); err != nil || fresh {
		t.Fatal("duplicate", err)
	}
	if err := os.WriteFile(moderationInboxPath(home, "scope"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := saveModerationNotice(home, "scope", "42", notice); err == nil {
		t.Fatal("corrupt inbox overwritten")
	}
}
func TestModerationInboxFailureDoesNotACK(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) { calls++; io.WriteString(out, `{"code":0,"data":{}}`) }))
	defer server.Close()
	watch := newCommissionWatch(t, server.URL, "pm_push")
	if err := os.Mkdir(moderationInboxPath(watch.home, watch.scope), 0700); err != nil {
		t.Fatal(err)
	}
	if err := watch.deliverCommissionNotifications(context.Background(), commissionTestPage(moderationTestNotification("42", "review")), "socket"); err == nil || calls != 0 {
		t.Fatal("ACK without inbox", err, calls)
	}
}
func TestLegacyStreamLeavesModerationForPMWatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/notifications/ack" {
			t.Error("moderation owned by PM watch acknowledged")
		}
		fmt.Fprintf(out, `{"code":0,"data":%s}`, commissionTestPage(moderationTestNotification("42", "review")))
	}))
	defer server.Close()
	watch := newCommissionWatch(t, server.URL, "pm_push")
	if err := dispatch.WriteJSON(dispatch.BindingPath(watch.home, watch.server.Name), watch.binding); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := drainCommissionNotifications(client.New(server.URL+"/api/v2", "token", "test", client.Meta{}), "json", "en", &output); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatal("stream stole moderation ownership")
	}
}

func TestModerationInboxSurvivesOutputFailureAndPlainWatch(t *testing.T) {
	acks := 0
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) { acks++; io.WriteString(out, `{"code":0,"data":{}}`) }))
	defer server.Close()
	watch := newCommissionWatch(t, server.URL, "pm_push")
	if err := os.Remove(dispatch.BindingPath(watch.home, watch.server.Name)); err != nil {
		t.Fatal(err)
	}
	watch.binding, watch.journal = nil, nil
	emissions := 0
	watch.emit = func(kind string, _ interface{}) error { emissions++; return errors.New("output closed") }
	page := commissionTestPage(moderationTestNotification("42", "review"))
	if err := watch.deliverSocketEvent(context.Background(), "notification_push", page); err == nil || acks != 0 {
		t.Fatal("unexpected ACK after output failure", err)
	}
	inbox, err := readModerationInbox(watch.home, watch.scope, "42")
	if err != nil || len(inbox.Notices) != 1 {
		t.Fatal("authoritative inbox lost", err)
	}
	// Restart/retry can ACK from the durable inbox without rerunning a host action.
	if err := watch.deliverCommissionNotifications(context.Background(), page, "http_poll"); err != nil {
		t.Fatal(err)
	}
	if emissions != 1 || acks != 1 {
		t.Fatal(emissions, acks)
	}
	var output strings.Builder
	command := watchCmd.Commands()
	for _, cmd := range command {
		if cmd.Name() == "notices" {
			cmd.SetOut(&output)
			defer cmd.SetOut(nil)
			if err := cmd.RunE(cmd, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !strings.Contains(output.String(), `"commission_id":"9007199254740993"`) {
		t.Fatal("watch notices lost receipt", output.String())
	}
}
func TestLegacyMalformedModerationDoesNotPreventOrderACK(t *testing.T) {
	acks := 0
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/notifications/pending" {
			fmt.Fprintf(out, `{"code":0,"data":%s}`, commissionTestPage(moderationTestNotification("43", "reject"), commissionTestNotification(82, 901, "42")))
			return
		}
		var body struct {
			Notifications []map[string]string `json:"notifications"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Notifications) != 1 || body.Notifications[0]["notification_id"] != "82" {
			t.Error(body)
		}
		acks++
		io.WriteString(out, `{"code":0,"data":{}}`)
	}))
	defer server.Close()
	watch := newCommissionWatch(t, server.URL, "pm_push")
	if err := os.Remove(dispatch.BindingPath(watch.home, watch.server.Name)); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := drainCommissionNotifications(client.New(server.URL+"/api/v2", "token", "test", client.Meta{}), "json", "en", &output); err != nil {
		t.Fatal(err)
	}
	if acks != 1 || strings.Contains(output.String(), moderationNotificationSource) {
		t.Fatal(acks, output.String())
	}
}

func TestModerationReceiptSurvivesPayloadEvictionAndLostACK(t *testing.T) {
	var lastACK []map[string]string
	failACK := true
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		var body struct {
			Notifications []map[string]string `json:"notifications"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		lastACK = body.Notifications
		if failACK {
			io.WriteString(out, `{"code":500,"data":{}}`)
			return
		}
		io.WriteString(out, `{"code":0,"data":{}}`)
	}))
	defer server.Close()
	watch := newCommissionWatch(t, server.URL, "pm_push")
	emissions := map[notificationID]int{}
	watch.emit = func(kind string, value interface{}) error {
		if kind != "commission_moderation_notification" {
			t.Fatalf("unexpected host event %s", kind)
		}
		emissions[value.(map[string]any)["notification"].(moderationNotification).ID]++
		return nil
	}
	makeNotice := func(id int) json.RawMessage {
		notice, _ := parseModerationNotification(moderationTestNotification("42", "review"), "42")
		notice.ID = notificationID(id)
		notice.Payload.NotificationVersion = int64(id)
		raw, _ := json.Marshal(notice)
		return raw
	}
	initial := []json.RawMessage{}
	for id := 1; id <= 100; id++ {
		initial = append(initial, makeNotice(id))
	}
	if err := watch.deliverCommissionNotifications(context.Background(), commissionTestPage(initial...), "socket"); err == nil {
		t.Fatal("lost ACK accepted")
	}
	failACK = false
	if err := watch.deliverCommissionNotifications(context.Background(), commissionTestPage(makeNotice(101)), "socket"); err != nil {
		t.Fatal(err)
	}
	inbox, err := readModerationInbox(watch.home, watch.scope, "42")
	if err != nil || len(inbox.Notices) != 100 || inbox.Notices[0].ID != 2 {
		t.Fatal("payload not evicted", err)
	}
	firstReceived := inbox.Receipts["1"]
	if err := watch.deliverCommissionNotifications(context.Background(), commissionTestPage(makeNotice(1), makeNotice(102)), "http_poll"); err != nil {
		t.Fatal(err)
	}
	if emissions[1] != 1 || emissions[102] != 1 || len(lastACK) != 2 || lastACK[0]["notification_id"] != "1" || lastACK[1]["notification_id"] != "102" {
		t.Fatal("duplicate or neighbor not acknowledged", emissions, lastACK)
	}
	inbox, err = readModerationInbox(watch.home, watch.scope, "42")
	if err != nil || len(inbox.Receipts) != 102 || inbox.Receipts["1"] != firstReceived {
		t.Fatal("receipt lost/refreshed", err)
	}
}

func TestModerationReceiptUpgradeAndExpiry(t *testing.T) {
	home := t.TempDir()
	notice, _ := parseModerationNotification(moderationTestNotification("42", "review"), "42")
	inbox := moderationInbox{Version: 1, Scope: "scope", Notices: []moderationNotification{notice}}
	if err := dispatch.WriteJSON(moderationInboxPath(home, "scope"), inbox); err != nil {
		t.Fatal(err)
	}
	if fresh, err := saveModerationNotice(home, "scope", "42", notice); err != nil || fresh {
		t.Fatal("legacy receipt duplicated", err)
	}
	inbox, err := readModerationInbox(home, "scope", "42")
	if err != nil || len(inbox.Receipts) != 1 {
		t.Fatal("upgrade not persisted", err)
	}
	original := time.Now().Add(-time.Hour).Unix()
	inbox.Receipts["81"] = original
	inbox.Receipts["1"] = time.Now().Add(-moderationReceiptRetention - time.Hour).Unix()
	if err := dispatch.WriteJSON(moderationInboxPath(home, "scope"), inbox); err != nil {
		t.Fatal(err)
	}
	if fresh, err := saveModerationNotice(home, "scope", "42", notice); err != nil || fresh {
		t.Fatal(err)
	}
	inbox, err = readModerationInbox(home, "scope", "42")
	if err != nil || len(inbox.Receipts) != 1 || inbox.Receipts["81"] != original {
		t.Fatal("wrong receipt retention", inbox, err)
	}
	inbox.Receipts["81"] = time.Now().Add(-moderationReceiptRetention - time.Hour).Unix()
	if err := dispatch.WriteJSON(moderationInboxPath(home, "scope"), inbox); err != nil {
		t.Fatal(err)
	}
	if fresh, err := saveModerationNotice(home, "scope", "42", notice); err != nil || !fresh {
		t.Fatal("expired receipt was not renewed", err)
	}
	inbox, err = readModerationInbox(home, "scope", "42")
	if err != nil || len(inbox.Notices) != 1 {
		t.Fatal("expired receipt corrupted retained payload", err)
	}
}
