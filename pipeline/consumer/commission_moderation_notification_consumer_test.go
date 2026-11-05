package consumer

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/notificationpayload"
	"eigenflux_server/rpc/notification/dal"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func moderationValues(t *testing.T) map[string]any {
	t.Helper()
	payload, _ := json.Marshal(notificationpayload.CommissionModeration{NotificationVersion: 2, ModerationID: "9007199254740993", CommissionID: "9007199254740995", RecipientAgentID: "9007199254740997", DraftVersion: 2, Status: "review", Decision: "review", Reason: "Please clarify the delivery.", OccurredAt: 1000})
	return map[string]any{"event_id": "9007199254740999", "schema_version": "1", "topic": notificationpayload.CommissionModerationTopic, "aggregate_type": "commission_moderation", "aggregate_id": "9007199254740993", "aggregate_version": "2", "occurred_at": "1000", "payload_json": string(payload)}
}

func TestModerationNotificationEnvelope(t *testing.T) {
	row, err := parseCommissionModerationNotification(moderationValues(t), 2000)
	if err != nil || row.AgentID != 9007199254740997 || row.OrderID != 0 || row.OrderVersion != 0 || row.SourceType != notificationpayload.SourceCommissionModeration {
		t.Fatalf("row: %#v %v", row, err)
	}
	for key, value := range map[string]any{"event_id": "0", "schema_version": "2", "aggregate_id": "9", "aggregate_version": "0", "aggregate_type": "commission", "occurred_at": "1001", "payload_json": "{}"} {
		t.Run(key, func(t *testing.T) {
			values := moderationValues(t)
			values[key] = value
			if _, err := parseCommissionModerationNotification(values, 2000); err == nil {
				t.Fatal("malformed event accepted")
			}
		})
	}
}

func TestModerationConsumerDurableDedupeAndRecipientIsolation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&dal.InboxNotification{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX inbox_dedupe ON notification_inbox(dedupe_key)").Error; err != nil {
		t.Fatal(err)
	}
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer rdb.Close()
	consumer := NewCommissionModerationNotificationConsumer(&config.Config{CommissionStream: "index", CommissionConsumerGroup: "indexer", CommissionDeadLetterStream: "dlq", CommissionNotificationConsumerWorkers: 1}, db, rdb)
	consumer.now = func() time.Time { return time.UnixMilli(2000) }
	if consumer.runtime.Stream != "index" || consumer.runtime.Group != "indexer:moderation-notification" || consumer.runtime.DeadLetterStream != "dlq:moderation-notification" {
		t.Fatal("consumer shares index ownership")
	}
	ctx := context.Background()
	values := moderationValues(t)
	for i := 0; i < 2; i++ {
		if got := consumer.Handle(ctx, "1-0", values); got != HandleSuccess {
			t.Fatalf("handle %d: %v", i, got)
		}
	}
	var count int64
	db.Model(&dal.InboxNotification{}).Count(&count)
	if count != 1 {
		t.Fatalf("duplicate rows: %d", count)
	}
	// A corrupt replay cannot redirect an already-persisted event to another Agent.
	var payload notificationpayload.CommissionModeration
	json.Unmarshal([]byte(values["payload_json"].(string)), &payload)
	payload.RecipientAgentID = "42"
	raw, _ := json.Marshal(payload)
	values["payload_json"] = string(raw)
	if got := consumer.Handle(ctx, "1-1", values); got != HandleRetry {
		t.Fatalf("recipient rewrite accepted: %v", got)
	}
	pending, err := dal.ListInboxNotifications(ctx, db, 42, 2000)
	if err != nil || len(pending) != 0 {
		t.Fatalf("foreign pending: %#v %v", pending, err)
	}
	// A later status at the same draft is a distinct fact, even if its wake-up fails.
	values = moderationValues(t)
	values["event_id"] = strconv.FormatInt(9007199254741000, 10)
	values["aggregate_version"] = "3"
	json.Unmarshal([]byte(values["payload_json"].(string)), &payload)
	payload.NotificationVersion = 3
	raw, _ = json.Marshal(payload)
	values["payload_json"] = string(raw)
	rdb.Close()
	if got := consumer.Handle(ctx, "1-2", values); got != HandleSuccess {
		t.Fatalf("durable insert failed with unavailable wake-up: %v", got)
	}
	pending, err = dal.ListInboxNotifications(ctx, db, 9007199254740997, 2000)
	if err != nil || len(pending) != 2 || pending[1].LastDeliveryError != "wake-up failed" {
		t.Fatalf("pending after wake failure: %#v %v", pending, err)
	}
	values["payload_json"] = "{}"
	if got := consumer.Handle(ctx, "1-3", values); got != HandleFailure {
		t.Fatal("malformed moderation fact not dead-lettered")
	}
}

func TestCommissionConsumersIgnoreOtherTopics(t *testing.T) {
	ctx := context.Background()
	moderation := &CommissionModerationNotificationConsumer{}
	if moderation.Handle(ctx, "", map[string]any{"topic": commissionPublishedTopic}) != HandleSuccess {
		t.Fatal("moderation consumer rejected catalogue event")
	}
	index := &CommissionIndexConsumer{}
	if index.Handle(ctx, "", map[string]any{"topic": []byte(notificationpayload.CommissionModerationTopic)}) != HandleSuccess {
		t.Fatal("index consumer tried moderation event")
	}
}
