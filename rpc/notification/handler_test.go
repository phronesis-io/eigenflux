package main

import (
	"context"
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
