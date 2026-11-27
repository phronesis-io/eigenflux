package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/desktopqueue"
	"cli.eigenflux.ai/internal/dispatch"
)

func manualReviewTestNotification(t *testing.T, id int64, role, status string) json.RawMessage {
	t.Helper()
	var fact desktopOrderFact
	if err := json.Unmarshal(commissionResultTestNotification(t, id, 901, "42", "order.validation.processing_error.v1", "validating"), &fact); err != nil {
		t.Fatal(err)
	}
	fact.Payload.RecipientRole = role
	fact.Payload.DeliveryReview = &dispatch.CommissionDeliveryReview{Status: status, SubmissionNumber: 1}
	raw, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCommissionManualReviewNotificationTerminalAndDesktop(t *testing.T) {
	for _, role := range []string{"buyer", "seller"} {
		for _, language := range []string{"zh", "en"} {
			t.Run(role+"/"+language, func(t *testing.T) {
				raw := manualReviewTestNotification(t, 82, role, "manual_review")
				var notification orderNotification
				if err := json.Unmarshal(raw, &notification); err != nil {
					t.Fatal(err)
				}
				want := "Awaiting manual platform review"
				if language == "zh" {
					want = "等待平台人工复核"
				}
				for _, envelope := range []string{"order.validation.processing_error.v1", "order.state.changed.v1"} {
					var output strings.Builder
					notification.Type = envelope
					if err := renderOrderNotification(&output, "table", language, notification); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(output.String(), want) {
						t.Errorf("terminal still reports a transient error: %s", output.String())
					}
				}
				legacy := notification.Payload
				legacy.EventKind, legacy.ToState = "", ""
				if _, got := localizedOrderNotificationText(language, "order.validation.processing_error.v1", legacy); got != want {
					t.Errorf("type/state fallback lost manual review: %s", got)
				}
				for _, platform := range []string{"darwin", "windows", "linux"} {
					message, err := commissionDesktopMessageForPlatform(raw, "https://example.com", "42", language, platform)
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(strings.ToLower(message.Title), strings.ToLower(want)) || strings.Contains(strings.ToLower(message.Title+message.Body), "retry") || strings.Contains(message.Title+message.Body, "重试") {
						t.Errorf("%s desktop still reports a transient error: %+v", platform, message)
					}
				}
				var output strings.Builder
				if err := renderOrderNotification(&output, "json", language, notification); err != nil {
					t.Fatal(err)
				}
				var encoded struct {
					Notification orderNotification `json:"notification"`
				}
				if err := json.Unmarshal([]byte(output.String()), &encoded); err != nil || encoded.Notification.Payload.DeliveryReview == nil || encoded.Notification.Payload.DeliveryReview.Status != "manual_review" || encoded.Notification.Payload.EventKind != "order.validation.processing_error.v1" {
					t.Fatalf("JSON lost original event/review metadata: %s (%v)", output.String(), err)
				}
			})
		}
	}
}

func TestCommissionManualReviewNotificationKeepsOtherResultPriority(t *testing.T) {
	for _, role := range []string{"buyer", "seller"} {
		for _, language := range []string{"zh", "en"} {
			for _, tc := range commissionResultTextCases {
				t.Run(role+"/"+language+"/"+tc.kind, func(t *testing.T) {
					payload := orderNotificationPayload{
						RecipientRole: role, ToState: "validating", EventKind: tc.kind,
						DeliveryReview: &dispatch.CommissionDeliveryReview{Status: "manual_review"},
					}
					if tc.kind == "order.validation.processing_error.v1" {
						payload.DeliveryReview.Status = "reviewing"
					}
					want := tc.en
					if language == "zh" {
						want = tc.zh
					}
					if _, got := localizedOrderNotificationText(language, "order.validation.processing_error.v1", payload); got != want {
						t.Errorf("result precedence changed: got %s, want %s", got, want)
					}
					if key := commissionDesktopCopyKey("order.validation.processing_error.v1", payload); key != tc.kind {
						t.Errorf("desktop result precedence changed: %s", key)
					}
				})
			}
		}
	}
}

func TestCommissionManualReviewNotificationSameVersionDelivery(t *testing.T) {
	for _, role := range []string{"buyer", "seller"} {
		t.Run(role, func(t *testing.T) {
			var reviewStatus atomic.Value
			reviewStatus.Store("reviewing")
			buyer, seller := "41", "42"
			if role == "buyer" {
				buyer, seller = "42", "43"
			}
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/v2/notifications/ack" && r.Method == http.MethodPost:
					_, _ = io.WriteString(out, `{"code":0,"data":{}}`)
				case r.URL.Path == "/api/v1/orders/901" && r.Method == http.MethodGet:
					materialTestResponse(out, map[string]any{"order": map[string]any{
						"order_id": "901", "version": 3, "state": "validating", "buyer_agent_id": buyer, "seller_agent_id": seller,
						"delivery_review": map[string]any{"status": reviewStatus.Load(), "submission_number": 1},
					}})
				case strings.HasPrefix(r.URL.Path, "/api/v2/public/agents/") && r.Method == http.MethodGet:
					materialTestResponse(out, map[string]any{"card": map[string]string{"agent_name": "Counterparty"}})
				default:
					t.Errorf("unexpected notification API request: %s %s", r.Method, r.URL.Path)
					http.NotFound(out, r)
				}
			}))
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			installCommissionIntakeRules(t, w)
			path := filepath.Join(t.TempDir(), "desktop.json")
			var err error
			w.desktop, err = desktopqueue.Open(path, w.scope)
			if err != nil {
				t.Fatal(err)
			}
			for index, status := range []string{"reviewing", "manual_review"} {
				reviewStatus.Store(status)
				id := int64(81 + index)
				page := commissionTestPage(manualReviewTestNotification(t, id, role, status))
				for _, transport := range []string{"socket", "http_poll"} {
					if err := w.deliverCommissionNotifications(context.Background(), page, transport); err != nil {
						t.Fatal(err)
					}
				}
				entry, ok := w.desktop.Next(time.Now())
				if !ok || entry.Message.ID != w.scope+":commission:"+strconv.FormatInt(id, 10) {
					t.Fatalf("same-version notification lost: %+v %v", entry, ok)
				}
				if status == "manual_review" && (strings.Contains(strings.ToLower(entry.Message.Title), "retry") || strings.Contains(entry.Message.Title, "重试")) {
					t.Fatalf("final desktop notice retained retry copy: %s", entry.Message.Title)
				}
				if err := w.desktop.Complete(entry.Message.ID, true, time.Now()); err != nil {
					t.Fatal(err)
				}
				queue, err := w.mainNoticeQueue()
				if err != nil {
					t.Fatal(err)
				}
				item, ok, err := queue.Next()
				if err != nil || !ok || item.ID != strconv.FormatInt(id, 10) {
					t.Fatalf("same-version main notice lost: %+v %v %v", item, ok, err)
				}
				prompt, err := w.commissionMainNoticePrompt(context.Background(), item)
				if err != nil {
					t.Fatal(err)
				}
				_, data, found := strings.Cut(prompt, "\n\nUNTRUSTED COMMISSION EVENT:\n")
				var payload struct {
					ID     string                             `json:"notification_id"`
					Event  string                             `json:"event_kind"`
					Role   string                             `json:"role"`
					Review *dispatch.CommissionDeliveryReview `json:"delivery_review"`
				}
				if !found || json.Unmarshal([]byte(data), &payload) != nil || payload.ID != item.ID || payload.Role != role || payload.Event != "order.validation.processing_error.v1" || payload.Review == nil || payload.Review.Status != status {
					t.Fatalf("main notice lost authoritative review status: %s", data)
				}
				if err := queue.Finish(item.ID, "delivered", ""); err != nil {
					t.Fatal(err)
				}
				// Persisted notification IDs survive restarts independently of the
				// unchanged order/version execution journal key.
				w.desktop, err = desktopqueue.Open(path, w.scope)
				if err != nil {
					t.Fatal(err)
				}
				w.mainNotices, err = dispatch.OpenMainNoticeQueue(*w.binding)
				if err != nil {
					t.Fatal(err)
				}
				if err := w.deliverCommissionNotifications(context.Background(), page, "http_poll"); err != nil {
					t.Fatal(err)
				}
				if pending, delivered := w.desktop.Counts(); pending != 0 || delivered != index+1 {
					t.Fatalf("duplicate desktop delivery: pending=%d delivered=%d", pending, delivered)
				}
				if _, ok, err := w.mainNotices.Next(); ok || err != nil {
					t.Fatalf("duplicate main delivery: %v %v", ok, err)
				}
			}
			if jobs := w.journal.Snapshot(); len(jobs) != 1 {
				t.Fatalf("notification delivery changed order/version execution dedupe: %+v", jobs)
			}
		})
	}
}
