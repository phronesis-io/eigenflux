package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/client"
)

func TestDrainOrderNotificationsRendersThenAcknowledges(t *testing.T) {
	ackCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifications/pending":
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"notifications":[{"notification_id":"41","source_type":"commission_order","type":"order.state.changed.v1","created_at":1700000000000,"payload":{"order_id":"9007199254740993","order_version":3,"recipient_role":"buyer","state":"delivered","to_state":"delivered","snapshot_id":"9007199254740995","occurred_at":1700000000000}}],"has_more":false}}`))
		case "/notifications/ack":
			ackCalls++
			var body map[string][]map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if got := body["notifications"][0]["notification_id"]; got != "41" {
				t.Fatalf("ack notification_id=%q", got)
			}
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"acknowledged":1}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	api := client.New(server.URL, "token", "test", client.Meta{})
	var output strings.Builder
	if err := drainOrderNotifications(api, "table", "zh", &output); err != nil {
		t.Fatal(err)
	}
	if ackCalls != 1 {
		t.Fatalf("ack calls=%d, want 1", ackCalls)
	}
	if got := output.String(); !strings.Contains(got, "订单 #9007199254740993 v3") || !strings.Contains(got, "交付待买方验收") {
		t.Fatalf("unexpected localized output %q", got)
	}
}

func TestDrainOrderNotificationsDoesNotAckInvalidPayload(t *testing.T) {
	ackCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/notifications/ack" {
			ackCalls++
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"notifications":[{"notification_id":"41","source_type":"commission_order","payload":{"order_id":7}}],"has_more":false}}`))
	}))
	defer server.Close()
	api := client.New(server.URL, "token", "test", client.Meta{})
	if err := drainOrderNotifications(api, "json", "en", &strings.Builder{}); err == nil {
		t.Fatal("invalid payload was accepted")
	}
	if ackCalls != 0 {
		t.Fatalf("invalid payload was acknowledged")
	}
}

func TestLocalizedOrderNotificationUnknownStateFallsBack(t *testing.T) {
	role, summary := localizedOrderNotification("en", "seller", "future_state")
	if role != "Seller" || summary != "Order updated" {
		t.Fatalf("fallback=(%q,%q)", role, summary)
	}
}

func TestLocalizedOrderNotificationCoversCommissionStates(t *testing.T) {
	states := []string{
		"preparing_materials", "awaiting_seller", "pending_payment", "in_progress", "validating",
		"awaiting_buyer_confirmation", "refund_pending", "refunded", "cancelled", "completed",
		"rejected", "expired",
	}
	for _, language := range []string{"zh", "en"} {
		fallback := "Order updated"
		if language == "zh" {
			fallback = "订单已更新"
		}
		for _, state := range states {
			t.Run(language+"/"+state, func(t *testing.T) {
				_, summary := localizedOrderNotification(language, "buyer", state)
				if summary == fallback {
					t.Fatalf("state %q used fallback copy", state)
				}
			})
		}
	}
}
