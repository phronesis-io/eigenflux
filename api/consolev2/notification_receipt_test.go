package consolev2

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"eigenflux_server/kitex_gen/eigenflux/base"
	notificationrpc "eigenflux_server/kitex_gen/eigenflux/notification"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/kitex/client/callopt"
)

type receiptNotificationClient struct {
	fakeNotificationClient
	fail bool
}

func (f *receiptNotificationClient) AckNotifications(ctx context.Context, req *notificationrpc.AckNotificationsReq, opts ...callopt.Option) (*notificationrpc.AckNotificationsResp, error) {
	if f.fail {
		return &notificationrpc.AckNotificationsResp{BaseResp: &base.BaseResp{Code: 500}}, nil
	}
	return f.fakeNotificationClient.AckNotifications(ctx, req, opts...)
}

func TestNotificationACKReceiptCompatibilityAndLogging(t *testing.T) {
	for _, tc := range []struct {
		name, source, kind string
		status             int
		fail, mixed        bool
	}{
		{name: "legacy", source: "commission_order", status: 200},
		{name: "display", source: "commission_order", kind: "displayed", status: 200},
		{name: "desktop", source: "commission_order", kind: "desktop_saved", status: 200},
		{name: "execution", source: "commission_order", kind: "execution_enqueued", status: 200},
		{name: "same_id_other_source", source: "commission_order", kind: "execution_enqueued", status: 200, mixed: true},
		{name: "legacy_other_source", source: "system", status: 200},
		{name: "invalid", source: "commission_order", kind: "private-path-token", status: 400},
		{name: "wrong_source", source: "system", kind: "execution_enqueued", status: 400},
		{name: "upstream_failed", source: "commission_order", kind: "execution_enqueued", status: 503, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs strings.Builder
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			defer slog.SetDefault(previous)
			client := &receiptNotificationClient{fail: tc.fail}
			svc := &Service{}
			svc.SetNotificationClient(client)
			h := server.Default()
			h.POST("/ack", func(ctx context.Context, c *app.RequestContext) {
				c.Set("agent_id", int64(42))
				svc.ackPendingNotifications(ctx, c)
			})
			item := map[string]interface{}{"notification_id": "9007199254740993", "source_type": tc.source}
			if tc.kind != "" {
				item["receipt_kind"] = tc.kind
			}
			items := []interface{}{item, item}
			if tc.mixed {
				items = append(items, map[string]interface{}{"notification_id": "9007199254740993", "source_type": "system"})
			}
			status, _, _ := performJSON(t, h, "POST", "/ack", map[string]interface{}{"notifications": items})
			if status != tc.status {
				t.Fatalf("status %d want %d", status, tc.status)
			}
			logged := strings.Contains(logs.String(), "notification_ack_receipt")
			wantLog := tc.status == 200 && tc.source == "commission_order"
			if logged != wantLog {
				t.Fatalf("receipt logging=%v want %v: %s", logged, wantLog, logs.String())
			}
			if wantLog {
				kind := tc.kind
				if kind == "" {
					kind = "unknown"
				}
				if !strings.Contains(logs.String(), `"receipt_kind":"`+kind+`"`) || !strings.Contains(logs.String(), `"notification_id":9007199254740993`) || !strings.Contains(logs.String(), `"evidence":"client_reported"`) {
					t.Fatal(logs.String())
				}
				if strings.Count(logs.String(), "notification_ack_receipt") != 1 {
					t.Fatal("duplicate receipt logs")
				}
			}
			if tc.status == 400 && client.acked.Load() != 0 {
				t.Fatal("invalid receipt acknowledged")
			}
			wantACK := int32(1)
			if tc.mixed {
				wantACK = 2
			}
			if tc.status == 200 && client.acked.Load() != wantACK {
				t.Fatal("ACK dedup changed")
			}
			if strings.Contains(logs.String(), "private-path-token") {
				t.Fatal("invalid receipt leaked into logs")
			}
		})
	}
}
