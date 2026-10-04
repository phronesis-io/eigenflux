package dal

import (
	"net"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresTopicStatusUsesMillisecondActivityTime(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for the PostgreSQL topic-status contract")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PG_DSN")
	}
	if ip := net.ParseIP(config.Host); config.Host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		t.Fatal("topic-status contract requires a loopback PostgreSQL instance")
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
	// Connection-local tables exercise real row locks and transactions without
	// modifying any conversations in the configured integration database.
	for _, statement := range []string{
		`CREATE TEMP TABLE conversations (
			conv_id BIGINT PRIMARY KEY, participant_a BIGINT NOT NULL, participant_b BIGINT NOT NULL,
			status SMALLINT NOT NULL, topic_status SMALLINT NOT NULL, updated_at BIGINT NOT NULL,
			msg_count INTEGER NOT NULL, origin_type TEXT NOT NULL)`,
		`CREATE TEMP TABLE conversation_topic_events (
			event_id BIGSERIAL PRIMARY KEY, conv_id BIGINT NOT NULL, actor_id BIGINT NOT NULL,
			previous_status SMALLINT NOT NULL, new_status SMALLINT NOT NULL, created_at BIGINT NOT NULL)`,
		`INSERT INTO conversations VALUES
			(1, 10, 20, 0, 1, 1788990000000, 1, 'broadcast'),
			(2, 10, 30, 0, 1, 1788999999000, 1, 'broadcast')`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}

	assertState := func(wantStatus int16, wantUpdatedAt int64, wantActors []int64) {
		t.Helper()
		conv, err := GetConversationByID(db, 1)
		if err != nil {
			t.Fatal(err)
		}
		if conv.TopicStatus != wantStatus || conv.UpdatedAt != wantUpdatedAt {
			t.Errorf("conversation status=%d updated_at=%d, want status=%d updated_at=%d", conv.TopicStatus, conv.UpdatedAt, wantStatus, wantUpdatedAt)
		}
		var events []ConversationTopicEvent
		if err := db.Order("event_id").Find(&events).Error; err != nil {
			t.Fatal(err)
		}
		if len(events) != len(wantActors) {
			t.Fatalf("topic event count=%d, want %d", len(events), len(wantActors))
		}
		for index, actor := range wantActors {
			previous := []int16{TopicStatusOpen, TopicStatusPendingVerify}[index]
			next := []int16{TopicStatusPendingVerify, TopicStatusClosed}[index]
			if event := events[index]; event.ConvID != 1 || event.ActorID != actor || event.PreviousStatus != previous || event.NewStatus != next || event.CreatedAt < 1000000000000 {
				t.Errorf("unexpected topic event: %+v", event)
			}
		}
		for _, actor := range []int64{10, 20} {
			conversations, err := ListConversations(db, actor, 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			var ids []int64
			for _, conversation := range conversations {
				ids = append(ids, conversation.ConvID)
			}
			wantCount := 1
			if actor == 10 {
				wantCount = 2
			}
			if len(ids) != wantCount || ids[0] != 1 {
				t.Errorf("recent conversation IDs for actor %d=%v, want %d conversations with changed conversation 1 first", actor, ids, wantCount)
			}
		}
	}

	previous, changed, err := UpdateConversationTopicStatus(db, 1, 10, TopicStatusPendingVerify)
	if err != nil || previous != TopicStatusOpen || !changed {
		t.Fatalf("topic update: previous=%d changed=%v err=%v", previous, changed, err)
	}
	firstUpdatedAt := clockNow.UnixMilli()
	assertState(TopicStatusPendingVerify, firstUpdatedAt, []int64{10})

	clockNow = clockNow.Add(time.Minute)
	previous, changed, err = UpdateConversationTopicStatus(db, 1, 20, TopicStatusPendingVerify)
	if err != nil || previous != TopicStatusPendingVerify || changed {
		t.Fatalf("no-op topic update: previous=%d changed=%v err=%v", previous, changed, err)
	}
	assertState(TopicStatusPendingVerify, firstUpdatedAt, []int64{10})

	previous, changed, err = UpdateConversationTopicStatus(db, 1, 20, TopicStatusClosed)
	if err != nil || previous != TopicStatusPendingVerify || !changed {
		t.Fatalf("second participant update: previous=%d changed=%v err=%v", previous, changed, err)
	}
	assertState(TopicStatusClosed, clockNow.UnixMilli(), []int64{10, 20})
}
