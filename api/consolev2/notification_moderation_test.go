package consolev2

import (
	"context"
	"eigenflux_server/kitex_gen/eigenflux/base"
	notificationrpc "eigenflux_server/kitex_gen/eigenflux/notification"
	"eigenflux_server/pkg/metrics"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	dto "github.com/prometheus/client_model/go"
	"strings"
	"testing"
)

func TestModerationNotificationProjection(t *testing.T) {
	raw := `{"notification_version":2,"moderation_id":"9007199254740993","commission_id":"9007199254740995","recipient_agent_id":"1","draft_version":2,"status":"review","decision":"review","flag":"claim","field":"title","reason":"Clarify title","occurred_at":1000}`
	for _, recipient := range []int64{1, 2} {
		t.Run(string(rune('0'+recipient)), func(t *testing.T) {
			client := &pendingFailureClient{response: &notificationrpc.ListPendingResp{BaseResp: &base.BaseResp{}, Notifications: []*notificationrpc.PendingNotification{{NotificationId: 9007199254740997, SourceType: "commission_moderation", PayloadJson: &raw, Content: "Service listing moderation: review — Clarify title"}}}}
			service := &Service{}
			service.SetNotificationClient(client)
			h := server.Default()
			h.GET("/pending", func(ctx context.Context, c *app.RequestContext) {
				c.Set("agent_id", recipient)
				service.listPendingNotifications(ctx, c)
			})
			status, response, _ := performJSON(t, h, "GET", "/pending", nil)
			if recipient == 2 {
				if status != 200 || len(response["data"].(map[string]interface{})["notifications"].([]interface{})) != 0 {
					t.Fatal("foreign payload exposed or poisoned the page")
				}
				return
			}
			if status != 200 {
				t.Fatalf("status=%d", status)
			}
			item := response["data"].(map[string]interface{})["notifications"].([]interface{})[0].(map[string]interface{})
			payload := item["payload"].(map[string]interface{})
			if payload["moderation_id"] != "9007199254740993" || payload["commission_id"] != "9007199254740995" || payload["recipient_agent_id"] != "1" {
				t.Fatalf("IDs changed: %#v", payload)
			}
			if item["issuer_identity"].(map[string]interface{})["subject_type"] != "platform" || item["action_authority"] != "none" {
				t.Fatal("moderation identity or authority changed")
			}
		})
	}
}

func TestModerationInvalidRowsDoNotHideOrderAPIProjection(t *testing.T) {
	valid := `{"notification_version":2,"moderation_id":"1","commission_id":"2","recipient_agent_id":"1","draft_version":2,"status":"review","decision":"review","flag":"","field":"title","reason":"private invalid reason","occurred_at":1000}`
	order := `{"order_id":9007199254740995}`
	for _, raw := range []string{"", "{", "{}", strings.Replace(valid, `"recipient_agent_id":"1"`, `"recipient_agent_id":"2"`, 1), strings.Replace(valid, `"status":"review"`, `"status":"future"`, 1)} {
		client := &pendingFailureClient{response: &notificationrpc.ListPendingResp{BaseResp: &base.BaseResp{}, Notifications: []*notificationrpc.PendingNotification{
			{NotificationId: 1, SourceType: "commission_moderation", PayloadJson: &raw},
			{NotificationId: 2, SourceType: "commission_order", PayloadJson: &order},
		}}}
		svc := &Service{}
		svc.SetNotificationClient(client)
		h := server.Default()
		h.GET("/pending", func(ctx context.Context, c *app.RequestContext) {
			c.Set("agent_id", int64(1))
			svc.listPendingNotifications(ctx, c)
		})
		counter := metrics.ModerationNotificationSkippedTotal.WithLabelValues("api")
		before, after := &dto.Metric{}, &dto.Metric{}
		if err := counter.Write(before); err != nil {
			t.Fatal(err)
		}
		status, response, _ := performJSON(t, h, "GET", "/pending", nil)
		if status != 200 {
			t.Fatalf("invalid moderation poisoned API: %d", status)
		}
		if err := counter.Write(after); err != nil {
			t.Fatal(err)
		}
		if got := after.GetCounter().GetValue() - before.GetCounter().GetValue(); got != 1 {
			t.Fatalf("skipped metric delta = %v, want 1", got)
		}
		items := response["data"].(map[string]interface{})["notifications"].([]interface{})
		if len(items) != 1 || items[0].(map[string]interface{})["source_type"] != "commission_order" {
			t.Fatal("valid Order lost behind invalid moderation")
		}
	}
}
