package dal

import (
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresTopicStatusUsesMillisecondActivityTime(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for the PostgreSQL topic-status contract")
	}
	clockNow := time.UnixMilli(1789000000123)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{NowFunc: func() time.Time { return clockNow }})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	// Connection-local tables keep the real row-lock and transaction semantics
	// without touching conversations in the shared integration database.
	for _, statement := range []string{
		`CREATE TEMP TABLE conversations (
			conv_id BIGINT PRIMARY KEY, participant_a BIGINT NOT NULL, participant_b BIGINT NOT NULL,
			status SMALLINT NOT NULL, topic_status SMALLINT NOT NULL, updated_at BIGINT NOT NULL)`,
		`CREATE TEMP TABLE conversation_topic_events (
			event_id BIGSERIAL PRIMARY KEY, conv_id BIGINT NOT NULL, actor_id BIGINT NOT NULL,
			previous_status SMALLINT NOT NULL, new_status SMALLINT NOT NULL, created_at BIGINT NOT NULL)`,
		`INSERT INTO conversations VALUES (1, 10, 20, 0, 1, 1788990000000)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}

	previous, changed, err := UpdateConversationTopicStatus(db, 1, 10, TopicStatusPendingVerify)
	if err != nil || previous != TopicStatusOpen || !changed {
		t.Fatalf("topic update: previous=%d changed=%v err=%v", previous, changed, err)
	}
	firstUpdatedAt := clockNow.UnixMilli()
	assertState := func(wantStatus int16, wantUpdatedAt, wantEvents int64) {
		t.Helper()
		conv, err := GetConversationByID(db, 1)
		if err != nil {
			t.Fatal(err)
		}
		if conv.TopicStatus != wantStatus || conv.UpdatedAt != wantUpdatedAt {
			t.Fatalf("conversation status=%d updated_at=%d, want status=%d updated_at=%d", conv.TopicStatus, conv.UpdatedAt, wantStatus, wantUpdatedAt)
		}
		var events int64
		if err := db.Model(&ConversationTopicEvent{}).Where("conv_id = ?", 1).Count(&events).Error; err != nil {
			t.Fatal(err)
		}
		if events != wantEvents {
			t.Fatalf("topic event count=%d, want %d", events, wantEvents)
		}
	}
	assertState(TopicStatusPendingVerify, firstUpdatedAt, 1)

	clockNow = clockNow.Add(time.Minute)
	previous, changed, err = UpdateConversationTopicStatus(db, 1, 20, TopicStatusPendingVerify)
	if err != nil || previous != TopicStatusPendingVerify || changed {
		t.Fatalf("no-op topic update: previous=%d changed=%v err=%v", previous, changed, err)
	}
	assertState(TopicStatusPendingVerify, firstUpdatedAt, 1)

	previous, changed, err = UpdateConversationTopicStatus(db, 1, 20, TopicStatusClosed)
	if err != nil || previous != TopicStatusPendingVerify || !changed {
		t.Fatalf("second participant update: previous=%d changed=%v err=%v", previous, changed, err)
	}
	assertState(TopicStatusClosed, clockNow.UnixMilli(), 2)
}
