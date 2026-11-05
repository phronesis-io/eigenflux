package notify_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	notificationrpc "eigenflux_server/kitex_gen/eigenflux/notification"
	"eigenflux_server/kitex_gen/eigenflux/notification/notificationservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/notificationpayload"
	"eigenflux_server/tests/testutil"
	"github.com/cloudwego/kitex/client"
	"github.com/redis/go-redis/v9"
)

func TestCommissionModerationStreamPendingAndAck(t *testing.T) {
	cfg := config.Load()
	if !cfg.EnableCommissionOrderNotifications {
		t.Skip("ENABLE_COMMISSION_ORDER_NOTIFICATIONS=true requires a running Pipeline moderation consumer")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	email := fmt.Sprintf("moderation-notice-%d@test.com", time.Now().UnixNano())
	testutil.EnsureLegacyTestAgent(t, email)
	var recipient int64
	if err := testutil.TestDB.QueryRow("SELECT agent_id FROM agents WHERE email=$1", email).Scan(&recipient); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testutil.TestDB.Exec("DELETE FROM agent_profiles WHERE agent_id=$1", recipient)
		testutil.TestDB.Exec("DELETE FROM agents WHERE agent_id=$1", recipient)
	})
	now := time.Now().UnixMilli()
	eventID := time.Now().UnixNano()
	payload := notificationpayload.CommissionModeration{NotificationVersion: 2, ModerationID: strconv.FormatInt(eventID-1, 10), CommissionID: strconv.FormatInt(eventID-2, 10), RecipientAgentID: strconv.FormatInt(recipient, 10), DraftVersion: 2, Status: "reject", Decision: "reject", Field: "title", Reason: "Please clarify the title.", OccurredAt: now}
	raw, _ := json.Marshal(payload)
	values := map[string]any{"event_id": strconv.FormatInt(eventID, 10), "schema_version": "1", "topic": notificationpayload.CommissionModerationTopic, "aggregate_type": "commission_moderation", "aggregate_id": payload.ModerationID, "aggregate_version": "2", "occurred_at": strconv.FormatInt(now, 10), "payload_json": string(raw)}
	rdb := testutil.GetRedisClient(t)
	for i := 0; i < 2; i++ {
		id, err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: cfg.CommissionStream, Values: values}).Result()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { rdb.XDel(context.Background(), cfg.CommissionStream, id) })
	}
	for {
		var count int
		err := testutil.TestDB.QueryRow("SELECT count(*) FROM notification_inbox WHERE notification_id=$1 AND agent_id=$2", eventID, recipient).Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("Pipeline did not persist moderation event")
		case <-time.After(100 * time.Millisecond):
		}
	}
	rpc, err := notificationservice.NewClient("NotificationService", client.WithHostPorts(fmt.Sprintf("127.0.0.1:%d", cfg.NotificationRPCPort)))
	if err != nil {
		t.Fatal(err)
	}
	pending, err := rpc.ListPending(ctx, &notificationrpc.ListPendingReq{AgentId: recipient})
	if err != nil || pending.BaseResp.Code != 0 {
		t.Fatalf("pending failed: %v", err)
	}
	found := false
	for _, notice := range pending.Notifications {
		if notice.NotificationId == eventID {
			found = true
			if notice.SourceType != "commission_moderation" || !strings.Contains(notice.Content, payload.Reason) {
				t.Fatal("moderation content missing")
			}
			decoded, err := notificationpayload.ParseCommissionModeration(notice.GetPayloadJson())
			if err != nil || decoded != payload {
				t.Fatalf("payload changed: %v", err)
			}
		}
	}
	if !found {
		t.Fatal("durable moderation notification missing")
	}
	ack := &notificationrpc.AckNotificationsReq{AgentId: recipient, Items: []*notificationrpc.AckNotificationItem{{NotificationId: eventID, SourceType: "commission_order"}}}
	rejected, err := rpc.AckNotifications(ctx, ack)
	if err != nil || rejected.BaseResp.Code == 0 {
		t.Fatal("wrong source ACK succeeded")
	}
	ack.Items[0].SourceType = "commission_moderation"
	for i := 0; i < 2; i++ {
		response, err := rpc.AckNotifications(ctx, ack)
		if err != nil || response.BaseResp.Code != 0 {
			t.Fatalf("ACK failed: %v", err)
		}
	}
	pending, err = rpc.ListPending(ctx, &notificationrpc.ListPendingReq{AgentId: recipient})
	if err != nil {
		t.Fatal(err)
	}
	for _, notice := range pending.Notifications {
		if notice.NotificationId == eventID {
			t.Fatal("ACKed moderation notification remained pending")
		}
	}
}

func TestCorruptModerationDoesNotBlockOrderPendingAndAck(t *testing.T) {
	// This test asserts exact pagination across all sources, so isolate persistent announcements too.
	cleanNotifyTestData(t)
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	email := fmt.Sprintf("moderation-isolation-%d@test.com", time.Now().UnixNano())
	testutil.EnsureLegacyTestAgent(t, email)
	var recipient int64
	if err := testutil.TestDB.QueryRow("SELECT agent_id FROM agents WHERE email=$1", email).Scan(&recipient); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testutil.TestDB.Exec("DELETE FROM agent_profiles WHERE agent_id=$1", recipient)
		testutil.TestDB.Exec("DELETE FROM agents WHERE agent_id=$1", recipient)
	})
	now := time.Now().UnixMilli()
	firstID := time.Now().UnixNano()
	for i := int64(0); i < 102; i++ {
		id := firstID + i
		source := notificationpayload.SourceCommissionModeration
		orderID, orderVersion := int64(0), int64(0)
		if i == 101 {
			source = "commission_order"
			orderID = 7
			orderVersion = 1
		}
		_, err := testutil.TestDB.Exec(`INSERT INTO notification_inbox(notification_id,agent_id,source_type,source_id,dedupe_key,event_kind,order_id,order_version,payload_json,occurred_at,received_at,expires_at) VALUES($1,$2,$3,$1,$4,'test.isolation',$5,$6,'{"status":"future_state"}',$7,$7,$8)`, id, recipient, source, fmt.Sprintf("isolation:%d", id), orderID, orderVersion, now, now+60000)
		if err != nil {
			t.Fatal(err)
		}
	}
	rpc, err := notificationservice.NewClient("NotificationService", client.WithHostPorts(fmt.Sprintf("127.0.0.1:%d", cfg.NotificationRPCPort)))
	if err != nil {
		t.Fatal(err)
	}
	limit := int32(1)
	pending, err := rpc.ListPending(ctx, &notificationrpc.ListPendingReq{AgentId: recipient, Limit: &limit})
	if err != nil || pending.BaseResp.Code != 0 || len(pending.Notifications) != 1 || pending.Notifications[0].NotificationId != firstID+101 || pending.GetHasMore() {
		if err != nil {
			t.Fatal(err)
		}
		source := ""
		if len(pending.Notifications) > 0 {
			source = pending.Notifications[0].SourceType
		}
		t.Fatalf("invalid moderation history blocked Order pagination: code=%d count=%d first_source=%s has_more=%t", pending.BaseResp.Code, len(pending.Notifications), source, pending.GetHasMore())
	}
	ack, err := rpc.AckNotifications(ctx, &notificationrpc.AckNotificationsReq{AgentId: recipient, Items: []*notificationrpc.AckNotificationItem{{NotificationId: firstID + 101, SourceType: "commission_order"}}})
	if err != nil || ack.BaseResp.Code != 0 {
		t.Fatal("invalid moderation history blocked Order ACK")
	}
	var retained int
	if err := testutil.TestDB.QueryRow("SELECT count(*) FROM notification_inbox WHERE agent_id=$1 AND source_type=$2 AND acknowledged_at IS NULL", recipient, notificationpayload.SourceCommissionModeration).Scan(&retained); err != nil || retained != 101 {
		t.Fatal("invalid rows were removed or acknowledged")
	}
	// Repairing retained history makes the fact deliverable without replay or reinsertion.
	payload := notificationpayload.CommissionModeration{NotificationVersion: 1, ModerationID: "1", CommissionID: "2", RecipientAgentID: strconv.FormatInt(recipient, 10), DraftVersion: 1, Status: "published", Decision: "pass", OccurredAt: now}
	raw, _ := json.Marshal(payload)
	if _, err := testutil.TestDB.Exec("UPDATE notification_inbox SET payload_json=$1 WHERE notification_id=$2", string(raw), firstID); err != nil {
		t.Fatal(err)
	}
	pending, err = rpc.ListPending(ctx, &notificationrpc.ListPendingReq{AgentId: recipient, Limit: &limit})
	if err != nil || pending.BaseResp.Code != 0 || len(pending.Notifications) != 1 || pending.Notifications[0].NotificationId != firstID {
		t.Fatal("repaired history did not become deliverable")
	}
}
