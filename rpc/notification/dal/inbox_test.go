package dal

import (
	"context"
	"eigenflux_server/pkg/notificationpayload"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupInboxTestDB(t *testing.T) *gorm.DB {
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
		acknowledged_at INTEGER NULL, last_delivery_error TEXT NOT NULL DEFAULT ''
	)`).Error; err != nil {
		t.Fatal(err)
	}
	return database
}

func TestSetInboxDeliveryErrorBoundsStoredMessage(t *testing.T) {
	database := setupInboxTestDB(t)
	ctx := context.Background()
	fixture := inboxFixture(12, 1, 1)
	if _, err := InsertInboxNotification(ctx, database, fixture); err != nil {
		t.Fatal(err)
	}
	if err := SetInboxDeliveryError(ctx, database, fixture.NotificationID, fixture.AgentID, strings.Repeat("错误", maxDeliveryErrorRunes+100)); err != nil {
		t.Fatal(err)
	}
	var row InboxNotification
	if err := database.First(&row, fixture.NotificationID).Error; err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(row.LastDeliveryError)); got != maxDeliveryErrorRunes {
		t.Fatalf("stored error length=%d, want %d", got, maxDeliveryErrorRunes)
	}
}

func inboxFixture(id, agent, version int64) *InboxNotification {
	return &InboxNotification{
		NotificationID: id, AgentID: agent, SourceType: SourceTypeCommissionOrder, SourceID: id,
		DedupeKey: "order:7:version:3:recipient:" + string(rune('0'+agent)), EventKind: "order.state.changed.v1",
		OrderID: 7, OrderVersion: version, PayloadJSON: `{"order_id":7,"state":"in_progress"}`,
		OccurredAt: 1000 + version, ReceivedAt: 2000, ExpiresAt: 3000,
	}
}

func TestInboxDedupeAuthorizationAndIdempotentAck(t *testing.T) {
	database := setupInboxTestDB(t)
	ctx := context.Background()
	first := inboxFixture(11, 1, 1)
	inserted, err := InsertInboxNotification(ctx, database, first)
	if err != nil || !inserted {
		t.Fatalf("first insert=(%v,%v)", inserted, err)
	}
	duplicate := *first
	duplicate.PayloadJSON = `{ "state": "in_progress", "order_id": 7 }`
	inserted, err = InsertInboxNotification(ctx, database, &duplicate)
	if err != nil || inserted {
		t.Fatalf("duplicate insert=(%v,%v)", inserted, err)
	}
	conflict := *first
	conflict.PayloadJSON = `{"order_id":7,"state":"completed"}`
	if _, err := InsertInboxNotification(ctx, database, &conflict); err == nil {
		t.Fatal("mismatched duplicate was accepted")
	}
	if err := AckInboxNotificationsBySource(ctx, database, 2, SourceTypeCommissionOrder, []int64{11}, 2500); err == nil {
		t.Fatal("another Agent acknowledged the notification")
	}
	if err := AckInboxNotificationsBySource(ctx, database, 1, SourceTypeCommissionOrder, []int64{11, 11}, 2500); err != nil {
		t.Fatal(err)
	}
	if err := AckInboxNotificationsBySource(ctx, database, 1, SourceTypeCommissionOrder, []int64{11}, 2600); err != nil {
		t.Fatalf("idempotent ACK failed: %v", err)
	}
	rows, err := ListInboxNotifications(ctx, database, 1, 2400)
	if err != nil || len(rows) != 0 {
		t.Fatalf("acknowledged notification remained pending: %#v, %v", rows, err)
	}
}

func TestModerationInboxSourceSpecificAckAndValidation(t *testing.T) {
	db := setupInboxTestDB(t)
	ctx := context.Background()
	row := inboxFixture(31, 1, 1)
	row.SourceType = notificationpayload.SourceCommissionModeration
	row.OrderID, row.OrderVersion = 0, 0
	if inserted, err := InsertInboxNotification(ctx, db, row); err != nil || !inserted {
		t.Fatalf("insert: %v %v", inserted, err)
	}
	if err := AckInboxNotificationsBySource(ctx, db, 1, SourceTypeCommissionOrder, []int64{31}, 2500); err == nil {
		t.Fatal("order source acknowledged moderation")
	}
	if err := AckInboxNotificationsBySource(ctx, db, 2, notificationpayload.SourceCommissionModeration, []int64{31}, 2500); err == nil {
		t.Fatal("foreign recipient acknowledged moderation")
	}
	if err := AckInboxNotificationsBySource(ctx, db, 1, notificationpayload.SourceCommissionModeration, []int64{31}, 2500); err != nil {
		t.Fatal(err)
	}
	if err := AckInboxNotificationsBySource(ctx, db, 1, notificationpayload.SourceCommissionModeration, []int64{31}, 2600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{notificationpayload.SourceCommissionModeration, SourceTypeCommissionOrder, "unknown"} {
		invalid := *row
		invalid.SourceType = source
		if source == notificationpayload.SourceCommissionModeration {
			invalid.OrderID = 7
		}
		if _, err := InsertInboxNotification(ctx, db, &invalid); err == nil {
			t.Fatalf("source-specific order constraints not enforced: %s", source)
		}
	}
}
