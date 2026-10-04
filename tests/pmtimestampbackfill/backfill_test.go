package pmtimestampbackfill

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"eigenflux_server/rpc/pm/dal"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const (
	activitySeconds int64 = 1789000000
	activityMillis  int64 = activitySeconds*1000 + 123
)

type database struct {
	ctx    context.Context
	config *pgx.ConnConfig
	admin  *pgx.Conn
	conn   *pgx.Conn
	up     string
	down   string
}

func migration(t *testing.T, name string) (string, string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate migration fixture")
	}
	text, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../migrations", name))
	if err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(string(text), "-- +goose Down")
	if !ok {
		t.Fatal("migration has no Down section")
	}
	strip := func(sql string) string {
		for _, marker := range []string{"-- +goose StatementBegin", "-- +goose StatementEnd"} {
			sql = strings.ReplaceAll(sql, marker, "")
		}
		return sql
	}
	return strip(up), strip(down)
}

func openDatabase(t *testing.T) *database {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for the PostgreSQL timestamp-backfill contract")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PG_DSN")
	}
	loopback := func(host string) bool {
		ip := net.ParseIP(host)
		return host == "localhost" || ip != nil && ip.IsLoopback()
	}
	if !loopback(config.Host) {
		t.Fatal("timestamp-backfill contract requires loopback PostgreSQL")
	}
	for _, fallback := range config.Fallbacks {
		if !loopback(fallback.Host) {
			t.Fatal("timestamp-backfill fallback must also use loopback PostgreSQL")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		_ = admin.Close(ctx)
		cancel()
		t.Fatal(err)
	}
	schema := "pm_timestamp_" + hex.EncodeToString(random[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		_ = admin.Close(ctx)
		cancel()
		t.Fatal(err)
	}
	db := &database{ctx: ctx, config: config.Copy(), admin: admin}
	db.config.RuntimeParams["search_path"] = schema
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if db.conn != nil {
			_ = db.conn.Close(cleanup)
		}
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("drop isolated schema: %v", err)
		}
		_ = admin.Close(cleanup)
		cancel()
	})
	db.conn = db.connect(t)
	initial, _ := migration(t, "000003_add_pm_tables.sql")
	db.exec(t, initial)
	db.exec(t, `CREATE TABLE conversation_topic_events (
		event_id BIGSERIAL PRIMARY KEY,
		conv_id BIGINT NOT NULL REFERENCES conversations(conv_id) ON DELETE CASCADE,
		actor_id BIGINT NOT NULL,
		previous_status SMALLINT NOT NULL CHECK (previous_status BETWEEN 0 AND 2),
		new_status SMALLINT NOT NULL CHECK (new_status BETWEEN 0 AND 2),
		created_at BIGINT NOT NULL);
		ALTER TABLE conversations ADD COLUMN topic_status SMALLINT NOT NULL DEFAULT 1
		CHECK (topic_status BETWEEN 0 AND 2);`)
	db.up, db.down = migration(t, "000112_backfill_conversation_topic_activity.sql")
	return db
}

func (db *database) connect(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.ConnectConfig(db.ctx, db.config.Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func (db *database) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := db.conn.Exec(db.ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func (db *database) seed(t *testing.T, id, timestamp int64, status, topic, count int) {
	t.Helper()
	db.exec(t, `INSERT INTO conversations
		(conv_id,participant_a,participant_b,initiator_id,last_sender_id,origin_type,
		origin_id,msg_count,status,topic_status,updated_at)
		VALUES($1,10,20,10,10,'broadcast',$1,$2,$3,$4,$5)`, id, count, status, topic, timestamp)
}

func (db *database) event(t *testing.T, id, actor int64, before, after int, timestamp int64) {
	t.Helper()
	db.exec(t, `INSERT INTO conversation_topic_events
		(conv_id,actor_id,previous_status,new_status,created_at) VALUES($1,$2,$3,$4,$5)`,
		id, actor, before, after, timestamp)
}

func (db *database) timestamp(t *testing.T, id int64) int64 {
	t.Helper()
	var timestamp int64
	if err := db.conn.QueryRow(db.ctx, "SELECT updated_at FROM conversations WHERE conv_id=$1", id).Scan(&timestamp); err != nil {
		t.Fatal(err)
	}
	return timestamp
}

func (db *database) apply(conn *pgx.Conn, sql string) error {
	tx, err := conn.Begin(db.ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(db.ctx, sql); err != nil {
		return err
	}
	return tx.Commit(db.ctx)
}

func TestPostgresBackfillRestoresBothParticipantsOrderingAndIsIdempotent(t *testing.T) {
	db := openDatabase(t)
	db.seed(t, 1, activitySeconds, 0, 2, 1)
	db.event(t, 1, 10, 1, 2, activityMillis)
	db.seed(t, 2, activityMillis-1000, 0, 2, 1)
	sqlDB := stdlib.OpenDB(*db.config.Copy())
	t.Cleanup(func() { _ = sqlDB.Close() })
	orm, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	assertOrder := func(recent, topic []int64) {
		t.Helper()
		for _, actor := range []int64{10, 20} {
			rows, err := dal.ListConversations(orm, actor, 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]int64, len(rows))
			for i, row := range rows {
				ids[i] = row.ConvID
			}
			if !reflect.DeepEqual(ids, recent) {
				t.Fatalf("actor %d recent=%v, want %v", actor, ids, recent)
			}
			rows, err = dal.ListConversationsByTopicStatus(orm, actor, nil, 10, "")
			if err != nil {
				t.Fatal(err)
			}
			for i, row := range rows {
				ids[i] = row.ConvID
			}
			if !reflect.DeepEqual(ids, topic) {
				t.Fatalf("actor %d topic=%v, want %v", actor, ids, topic)
			}
		}
	}
	assertOrder([]int64{2, 1}, []int64{1, 2})
	for repeat := 0; repeat < 2; repeat++ {
		if err := db.apply(db.conn, db.up); err != nil {
			t.Fatal(err)
		}
		if got := db.timestamp(t, 1); got != activityMillis {
			t.Fatalf("updated_at=%d, want audited event %d", got, activityMillis)
		}
		assertOrder([]int64{1, 2}, []int64{2, 1})
	}
	var events int
	if err := db.conn.QueryRow(db.ctx, "SELECT count(*) FROM conversation_topic_events").Scan(&events); err != nil || events != 1 {
		t.Fatalf("migration changed topic events: count=%d err=%v", events, err)
	}
	if err := db.apply(db.conn, db.down); err == nil {
		t.Fatal("Down must reject destructive timestamp rollback")
	}
	if got := db.timestamp(t, 1); got != activityMillis {
		t.Fatal("failed Down changed repaired activity")
	}
}

func TestPostgresBackfillUsesLatestEventIDAndSkipsAmbiguousRows(t *testing.T) {
	db := openDatabase(t)
	db.seed(t, 1, activitySeconds, 0, 2, 1)
	// A clock correction can reverse event times. The newest event ID wins.
	db.event(t, 1, 10, 1, 2, activityMillis+300)
	db.event(t, 1, 20, 2, 1, activityMillis+200)
	db.event(t, 1, 10, 1, 2, activityMillis)
	unchanged := map[int64]int64{}
	for index, name := range []string{"closed", "no-event", "mismatch", "newer-message", "milliseconds",
		"empty", "future-event", "outsider", "no-op", "other-second", "seconds-event"} {
		id := int64(index + 10)
		timestamp, status, count := activitySeconds, 0, 1
		if name == "closed" {
			status = 2
		} else if name == "milliseconds" {
			timestamp = activityMillis + 500
		} else if name == "empty" {
			count = 0
		} else if name == "future-event" {
			timestamp = time.Now().Unix() + 3600
		}
		db.seed(t, id, timestamp, status, 2, count)
		unchanged[id] = timestamp
		if name == "no-event" {
			continue
		}
		actor, before, after, eventTime := int64(10), 1, 2, activityMillis
		switch name {
		case "mismatch":
			after = 0
		case "outsider":
			// An earlier valid event cannot authorize falling back past the latest.
			db.event(t, id, 10, 1, 2, activityMillis)
			actor = 99
		case "no-op":
			before = 2
		case "other-second":
			eventTime += 1000
		case "seconds-event":
			eventTime = activitySeconds
		case "future-event":
			eventTime = timestamp*1000 + 123
		}
		db.event(t, id, actor, before, after, eventTime)
		if name == "newer-message" {
			db.exec(t, `INSERT INTO private_messages(msg_id,conv_id,sender_id,receiver_id,content,created_at)
				VALUES($1,$1,10,20,'fixture',$2)`, id, activityMillis+1)
		}
	}
	if err := db.apply(db.conn, db.up); err != nil {
		t.Fatal(err)
	}
	if got := db.timestamp(t, 1); got != activityMillis {
		t.Fatalf("used max timestamp instead of latest event ID: got %d", got)
	}
	for id, want := range unchanged {
		if got := db.timestamp(t, id); got != want {
			t.Errorf("ambiguous conversation %d updated_at=%d, want unchanged %d", id, got, want)
		}
	}
}

func (db *database) waitBlocked(t *testing.T, waiter, blocker uint32) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := db.admin.QueryRow(db.ctx, "SELECT $1::int=ANY(pg_blocking_pids($2::int))", blocker, waiter).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("expected a real PostgreSQL row-lock wait")
		}
	}
}

func TestPostgresBackfillDoesNotOverwriteActivityCommittedWhileItWaits(t *testing.T) {
	db := openDatabase(t)
	db.seed(t, 1, activitySeconds, 0, 2, 1)
	db.event(t, 1, 10, 1, 2, activityMillis)
	writer := db.connect(t)
	tx, err := writer.Begin(db.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	newActivity := activityMillis + 10000
	if _, err := tx.Exec(db.ctx, "UPDATE conversations SET updated_at=$1 WHERE conv_id=1", newActivity); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- db.apply(db.conn, db.up) }()
	db.waitBlocked(t, db.conn.PgConn().PID(), writer.PgConn().PID())
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := db.timestamp(t, 1); got != newActivity {
		t.Fatalf("migration overwrote concurrent activity: got %d, want %d", got, newActivity)
	}
}

func TestPostgresTopicUpdateWaitingBehindBackfillPreservesItsNewActivity(t *testing.T) {
	db := openDatabase(t)
	db.seed(t, 1, activitySeconds, 0, 2, 1)
	db.event(t, 1, 10, 1, 2, activityMillis)
	tx, err := db.conn.Begin(db.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(db.ctx, db.up); err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	sqlDB := stdlib.OpenDB(*db.config.Copy())
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	orm, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		NowFunc: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	var pid uint32
	if err := sqlDB.QueryRowContext(db.ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		previous, changed, err := dal.UpdateConversationTopicStatus(orm.WithContext(db.ctx), 1, 20, dal.TopicStatusOpen)
		if err == nil && (previous != dal.TopicStatusClosed || !changed) {
			err = fmt.Errorf("topic update previous=%d changed=%v", previous, changed)
		}
		done <- err
	}()
	db.waitBlocked(t, pid, db.conn.PgConn().PID())
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := db.apply(db.conn, db.up); err != nil {
		t.Fatal(err)
	}
	if got := db.timestamp(t, 1); got != clock.UnixMilli() {
		t.Fatalf("new topic activity=%d, want %d", got, clock.UnixMilli())
	}
	var status, events int
	if err := db.conn.QueryRow(db.ctx, "SELECT topic_status FROM conversations WHERE conv_id=1").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := db.conn.QueryRow(db.ctx, "SELECT count(*) FROM conversation_topic_events WHERE conv_id=1").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if status != int(dal.TopicStatusOpen) || events != 2 {
		t.Fatalf("new topic state=%d events=%d", status, events)
	}
}
