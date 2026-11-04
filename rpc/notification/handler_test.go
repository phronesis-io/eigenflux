package main

import (
	"context"
	"eigenflux_server/pkg/notificationpayload"
	"fmt"
	"strings"
	"testing"

	"eigenflux_server/kitex_gen/eigenflux/notification"
	"eigenflux_server/rpc/notification/dal"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func notificationServiceFixture(t *testing.T) (*NotificationServiceImpl, *gorm.DB) {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`CREATE TABLE notification_inbox (
		notification_id INTEGER PRIMARY KEY, agent_id INTEGER NOT NULL, source_type TEXT NOT NULL,
		source_id INTEGER NOT NULL, dedupe_key TEXT NOT NULL UNIQUE, event_kind TEXT NOT NULL,
		order_id INTEGER NOT NULL, order_version INTEGER NOT NULL, payload_json TEXT NOT NULL,
		occurred_at INTEGER NOT NULL, received_at INTEGER NOT NULL, expires_at INTEGER NOT NULL,
		acknowledged_at INTEGER NULL, last_delivery_error TEXT NOT NULL DEFAULT '')`).Error; err != nil {
		t.Fatal(err)
	}
	redisServer := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewNotificationServiceImpl(database, rdb), database
}

func TestCommissionOrderListAndStrongAck(t *testing.T) {
	service, database := notificationServiceFixture(t)
	ctx := context.Background()
	row := &dal.InboxNotification{
		NotificationID: 11, AgentID: 1, SourceType: dal.SourceTypeCommissionOrder, SourceID: 11,
		DedupeKey: "order:7:version:1:recipient:1:event:order.state.changed.v1", EventKind: "order.state.changed.v1",
		OrderID: 7, OrderVersion: 1, PayloadJSON: `{"order_id":7,"order_version":1}`,
		OccurredAt: 1000, ReceivedAt: 1001, ExpiresAt: 9999999999999,
	}
	if inserted, err := dal.InsertInboxNotification(ctx, database, row); err != nil || !inserted {
		t.Fatalf("insert=(%v,%v)", inserted, err)
	}
	limit := int32(1)
	response, err := service.ListPending(ctx, &notification.ListPendingReq{AgentId: 1, Limit: &limit})
	if err != nil || response.BaseResp.Code != 0 || len(response.Notifications) != 1 || response.Notifications[0].PayloadJson == nil {
		t.Fatalf("list response=%#v err=%v", response, err)
	}
	foreign, err := service.AckNotifications(ctx, &notification.AckNotificationsReq{AgentId: 2, Items: []*notification.AckNotificationItem{{NotificationId: 11, SourceType: dal.SourceTypeCommissionOrder}}})
	if err != nil || foreign.BaseResp.Code == 0 {
		t.Fatalf("foreign ACK response=%#v err=%v", foreign, err)
	}
	ack, err := service.AckNotifications(ctx, &notification.AckNotificationsReq{AgentId: 1, Items: []*notification.AckNotificationItem{{NotificationId: 11, SourceType: dal.SourceTypeCommissionOrder}}})
	if err != nil || ack.BaseResp.Code != 0 {
		t.Fatalf("ACK response=%#v err=%v", ack, err)
	}
	retry, _ := service.AckNotifications(ctx, &notification.AckNotificationsReq{AgentId: 1, Items: []*notification.AckNotificationItem{{NotificationId: 11, SourceType: dal.SourceTypeCommissionOrder}}})
	if retry.BaseResp.Code != 0 {
		t.Fatalf("idempotent ACK response=%#v", retry)
	}
}

func TestPendingCursorRoundTrip(t *testing.T) {
	item := &notification.PendingNotification{NotificationId: 9, SourceType: "commission_order", CreatedAt: 8}
	cursor, err := decodePendingCursor(encodePendingCursor(item))
	if err != nil || cursor.NotificationID != 9 || !pendingAfterCursor(&notification.PendingNotification{NotificationId: 10, SourceType: "commission_order", CreatedAt: 8}, *cursor) {
		t.Fatalf("cursor=%#v err=%v", cursor, err)
	}
}

func TestModerationPendingContentAndAtomicSourceAck(t *testing.T) {
	service, db := notificationServiceFixture(t)
	ctx := context.Background()
	raw := `{"notification_version":2,"moderation_id":"9007199254740993","commission_id":"9007199254740995","recipient_agent_id":"1","draft_version":2,"status":"reject","decision":"reject","flag":"claim","field":"title","reason":"请修改标题","occurred_at":1000}`
	row := &dal.InboxNotification{NotificationID: 31, AgentID: 1, SourceType: notificationpayload.SourceCommissionModeration, SourceID: 31, DedupeKey: "moderation:1", EventKind: notificationpayload.CommissionModerationTopic, PayloadJSON: raw, OccurredAt: 1000, ReceivedAt: 1001, ExpiresAt: 9999999999999}
	if _, err := dal.InsertInboxNotification(ctx, db, row); err != nil {
		t.Fatal(err)
	}
	response, err := service.ListPending(ctx, &notification.ListPendingReq{AgentId: 1})
	if err != nil || response.BaseResp.Code != 0 || len(response.Notifications) != 1 {
		t.Fatalf("pending: %#v %v", response, err)
	}
	n := response.Notifications[0]
	if n.SourceType != notificationpayload.SourceCommissionModeration || n.GetPayloadJson() != raw || !strings.Contains(n.Content, "reject") || !strings.Contains(n.Content, "请修改标题") {
		t.Fatalf("projection: %#v", n)
	}
	foreign, _ := service.ListPending(ctx, &notification.ListPendingReq{AgentId: 2})
	if len(foreign.Notifications) != 0 {
		t.Fatal("foreign recipient can read moderation")
	}
	for _, req := range []*notification.AckNotificationsReq{
		{AgentId: 2, Items: []*notification.AckNotificationItem{{NotificationId: 31, SourceType: notificationpayload.SourceCommissionModeration}}},
		{AgentId: 1, Items: []*notification.AckNotificationItem{{NotificationId: 31, SourceType: dal.SourceTypeCommissionOrder}}},
	} {
		response, _ := service.AckNotifications(ctx, req)
		if response.BaseResp.Code == 0 {
			t.Fatal("invalid ACK succeeded")
		}
	}
	ack := &notification.AckNotificationsReq{AgentId: 1, Items: []*notification.AckNotificationItem{{NotificationId: 31, SourceType: notificationpayload.SourceCommissionModeration}}}
	for i := 0; i < 2; i++ {
		response, _ := service.AckNotifications(ctx, ack)
		if response.BaseResp.Code != 0 {
			t.Fatalf("ACK failed: %#v", response)
		}
	}
	response, _ = service.ListPending(ctx, &notification.ListPendingReq{AgentId: 1})
	if len(response.Notifications) != 0 {
		t.Fatal("acknowledged notice remained pending")
	}
}

func TestMixedDurableAckRollsBackOnForeignModeration(t *testing.T) {
	service, db := notificationServiceFixture(t)
	ctx := context.Background()
	order := &dal.InboxNotification{NotificationID: 41, AgentID: 1, SourceType: dal.SourceTypeCommissionOrder, SourceID: 41, DedupeKey: "order:41", EventKind: "order.state.changed.v1", OrderID: 1, OrderVersion: 1, PayloadJSON: "{}", OccurredAt: 1000, ReceivedAt: 1001, ExpiresAt: 9999999999999}
	foreign := *order
	foreign.NotificationID = 42
	foreign.AgentID = 2
	foreign.SourceID = 42
	foreign.SourceType = notificationpayload.SourceCommissionModeration
	foreign.OrderID = 0
	foreign.OrderVersion = 0
	foreign.DedupeKey = "moderation:42"
	for _, row := range []*dal.InboxNotification{order, &foreign} {
		if _, err := dal.InsertInboxNotification(ctx, db, row); err != nil {
			t.Fatal(err)
		}
	}
	response, err := service.AckNotifications(ctx, &notification.AckNotificationsReq{AgentId: 1, Items: []*notification.AckNotificationItem{{NotificationId: 41, SourceType: dal.SourceTypeCommissionOrder}, {NotificationId: 42, SourceType: notificationpayload.SourceCommissionModeration}}})
	if err != nil || response.BaseResp.Code == 0 {
		t.Fatal("foreign mixed ACK succeeded")
	}
	var saved dal.InboxNotification
	if err := db.First(&saved, 41).Error; err != nil {
		t.Fatal(err)
	}
	if saved.AcknowledgedAt != nil {
		t.Fatal("partial ACK committed before foreign ownership failure")
	}
}

func TestInvalidModerationDoesNotBlockPendingPaginationOrOrderAck(t *testing.T) {
	service, db := notificationServiceFixture(t)
	ctx := context.Background()
	for i := int64(1); i <= 101; i++ {
		raw := `{"notification_version":1,"moderation_id":"1","commission_id":"2","recipient_agent_id":"1","draft_version":1,"status":"future_state","decision":"","flag":"","field":"","reason":"private malformed record","occurred_at":1000}`
		row := &dal.InboxNotification{NotificationID: i, AgentID: 1, SourceType: notificationpayload.SourceCommissionModeration, SourceID: i, DedupeKey: fmt.Sprintf("bad:%d", i), EventKind: notificationpayload.CommissionModerationTopic, PayloadJSON: raw, OccurredAt: 1000, ReceivedAt: 1001, ExpiresAt: 9999999999999}
		if _, err := dal.InsertInboxNotification(ctx, db, row); err != nil {
			t.Fatal(err)
		}
	}
	order := &dal.InboxNotification{NotificationID: 900, AgentID: 1, SourceType: dal.SourceTypeCommissionOrder, SourceID: 900, DedupeKey: "valid:order", EventKind: "order.state.changed.v1", OrderID: 7, OrderVersion: 1, PayloadJSON: `{"order_id":"7"}`, OccurredAt: 2000, ReceivedAt: 2001, ExpiresAt: 9999999999999}
	moderation := *order
	moderation.NotificationID = 901
	moderation.SourceID = 901
	moderation.SourceType = notificationpayload.SourceCommissionModeration
	moderation.OrderID = 0
	moderation.OrderVersion = 0
	moderation.DedupeKey = "valid:moderation"
	moderation.OccurredAt = 2001
	moderation.PayloadJSON = `{"notification_version":2,"moderation_id":"1","commission_id":"2","recipient_agent_id":"1","draft_version":1,"status":"published","decision":"pass","flag":"","field":"","reason":"","occurred_at":2001}`
	for _, row := range []*dal.InboxNotification{order, &moderation} {
		if _, err := dal.InsertInboxNotification(ctx, db, row); err != nil {
			t.Fatal(err)
		}
	}
	limit := int32(1)
	first, err := service.ListPending(ctx, &notification.ListPendingReq{AgentId: 1, Limit: &limit})
	if err != nil || first.BaseResp.Code != 0 || len(first.Notifications) != 1 || first.Notifications[0].NotificationId != 900 || !first.GetHasMore() {
		t.Fatalf("invalid rows blocked first page: %#v %v", first, err)
	}
	cursor := first.GetNextCursor()
	next, err := service.ListPending(ctx, &notification.ListPendingReq{AgentId: 1, Limit: &limit, Cursor: &cursor})
	if err != nil || next.BaseResp.Code != 0 || len(next.Notifications) != 1 || next.Notifications[0].NotificationId != 901 || next.GetHasMore() {
		t.Fatalf("next page: %#v %v", next, err)
	}
	ack, err := service.AckNotifications(ctx, &notification.AckNotificationsReq{AgentId: 1, Items: []*notification.AckNotificationItem{{NotificationId: 900, SourceType: dal.SourceTypeCommissionOrder}}})
	if err != nil || ack.BaseResp.Code != 0 {
		t.Fatal("corrupt moderation blocked Order ACK")
	}
	var retained int64
	if err := db.Model(&dal.InboxNotification{}).Where("notification_id <= 101 AND acknowledged_at IS NULL").Count(&retained).Error; err != nil || retained != 101 {
		t.Fatalf("invalid history was lost or acknowledged: %d %v", retained, err)
	}
}
