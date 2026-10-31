package integrationfeed

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"eigenflux_server/pkg/integrationidentity"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func feedStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("INTEGRATION_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("INTEGRATION_TEST_PG_DSN required (isolated loopback PostgreSQL)")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("explicit loopback PostgreSQL DSN required")
	}
	open := func(dsn string) *gorm.DB {
		t.Helper()
		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	admin := open(dsn)
	adminConn, _ := admin.DB()
	t.Cleanup(func() { _ = adminConn.Close() })
	schema := "feed_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error })
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db := open(u.String())
	conn, _ := db.DB()
	conn.SetMaxOpenConns(12)
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.Exec(`CREATE TABLE agents(agent_id BIGINT PRIMARY KEY)`).Error; err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"000125_enterprise_integrations.sql", "000126_integration_contexts.sql", "000127_integration_feed_batches.sql"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		up, _, ok := strings.Cut(string(raw), "-- +goose Down")
		if !ok {
			t.Fatal("missing rollback")
		}
		if err := db.Exec(up).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, sql := range []string{
		`UPDATE integration_control SET enabled=true`,
		`INSERT INTO agents VALUES(1),(2)`,
		`INSERT INTO integration_apps(app_id,name,enabled,credential_hash,max_agents,min_poll_interval_seconds,created_at,updated_at) VALUES('partner','Partner',true,'hash',2,3600,0,0)`,
		`INSERT INTO integration_agents(agent_id,app_id,external_agent_id,runtime_name,runtime_version,created_at,updated_at) VALUES(1,'partner','one','jarvis','2',0,0),(2,'partner','two','jarvis','2',0,0)`,
		`INSERT INTO integration_matching_profiles(agent_id,source_revision,profile_version,content_hash,keywords,embedding,embedding_model,updated_at) VALUES(1,1,1,'hash','[]',decode('0000803f','hex'),'test',0),(2,1,1,'hash','[]',decode('0000803f','hex'),'test',0)`,
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	return &Store{DB: db}
}

func requireError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var typed *integrationidentity.Error
	if !errors.As(err, &typed) || typed.Status != status || typed.Code != code {
		t.Fatalf("got %v, want %d/%s", err, status, code)
	}
}

func TestPostgresSingleGenerationAndDurableRetry(t *testing.T) {
	s := feedStore(t)
	ctx := context.Background()
	var admitted atomic.Int64
	var wg sync.WaitGroup
	winner := make(chan Batch, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			batch, accepted, err := s.Begin(ctx, 1, Request{Sequence: 1})
			if accepted {
				admitted.Add(1)
				winner <- batch
			} else {
				requireError(t, err, 409, "FEED_GENERATION_IN_PROGRESS")
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatal("concurrent admission", admitted.Load())
	}
	batch := <-winner
	payload := json.RawMessage(`{"schema_version":"integration.feed.v1","items":[{"item_id":"123"}]}`)
	applied, err := s.Complete(ctx, batch, payload, 1)
	if err != nil || !applied {
		t.Fatal(applied, err)
	}
	// A new Store instance has no process memory and still returns this batch.
	restarted := &Store{DB: s.DB}
	retry, newRound, err := restarted.Begin(ctx, 1, Request{Sequence: 1, Limit: 20})
	if err != nil || newRound || retry.BatchID != batch.BatchID || retry.Payload != string(payload) {
		// JSONB canonicalizes spacing; compare parsed content below instead.
		if err != nil || newRound || retry.BatchID != batch.BatchID {
			t.Fatal(retry, newRound, err)
		}
	}
	var saved map[string]any
	if err := json.Unmarshal([]byte(retry.Payload), &saved); err != nil || len(saved["items"].([]any)) != 1 {
		t.Fatal(saved, err)
	}
	_, _, err = s.Begin(ctx, 1, Request{Sequence: 2})
	requireError(t, err, 429, "FEED_POLL_TOO_EARLY")
	var retryErr interface{ RetryAfter() int }
	if !errors.As(err, &retryErr) || retryErr.RetryAfter() < 3500 {
		t.Fatal("retry delay missing", err)
	}
	_, _, err = s.Begin(ctx, 1, Request{Sequence: 1, Limit: 1})
	requireError(t, err, 409, "FEED_REQUEST_CONFLICT")
	if err := s.DB.Exec(`UPDATE integration_agents SET next_poll_at=0 WHERE agent_id=1`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec(`UPDATE integration_feed_batches SET created_at=created_at-3600000 WHERE agent_id=1`).Error; err != nil {
		t.Fatal(err)
	}
	second, accepted, err := s.Begin(ctx, 1, Request{Sequence: 2})
	if err != nil || !accepted {
		t.Fatal(second, accepted, err)
	}
	_, _, err = s.Begin(ctx, 1, Request{Sequence: 1})
	requireError(t, err, 409, "FEED_SEQUENCE_STALE")
	if applied, err := s.Complete(ctx, batch, payload, 1); err != nil || applied {
		t.Fatal("old completion overwrote batch", applied, err)
	}
	var used int64
	if err := s.DB.Table("integration_apps").Select("feed_batches_total").Where("app_id='partner'").Scan(&used).Error; err != nil || used != 2 {
		t.Fatal(used, err)
	}
}

func TestPostgresAppQuotaSharedAcrossAgentsAndCadence(t *testing.T) {
	s := feedStore(t)
	ctx := context.Background()
	if err := s.DB.Exec(`UPDATE integration_apps SET max_feed_batches_per_day=1`).Error; err != nil {
		t.Fatal(err)
	}
	if _, accepted, err := s.Begin(ctx, 1, Request{Sequence: 1}); err != nil || !accepted {
		t.Fatal(accepted, err)
	}
	_, _, err := s.Begin(ctx, 2, Request{Sequence: 1})
	requireError(t, err, 429, "APPLICATION_FEED_QUOTA_EXCEEDED")
	// Agent and application counters roll back together on failed admission.
	var next int64
	if err := s.DB.Table("integration_agents").Select("next_poll_at").Where("agent_id=2").Scan(&next).Error; err != nil || next != 0 {
		t.Fatal(next, err)
	}
	if err := s.DB.Exec(`UPDATE integration_apps SET feed_usage_day=feed_usage_day-1`).Error; err != nil {
		t.Fatal(err)
	}
	if _, accepted, err := s.Begin(ctx, 2, Request{Sequence: 1}); err != nil || !accepted {
		t.Fatal(accepted, err)
	}
}

func TestPostgresUnknownNeverRegeneratesAndDisableProtectsPayload(t *testing.T) {
	s := feedStore(t)
	ctx := context.Background()
	batch, accepted, err := s.Begin(ctx, 1, Request{Sequence: 1})
	if err != nil || !accepted {
		t.Fatal(accepted, err)
	}
	if err := s.DB.Exec(`UPDATE integration_feed_batches SET lease_until=0 WHERE agent_id=1`).Error; err != nil {
		t.Fatal(err)
	}
	_, accepted, err = s.Begin(ctx, 1, Request{Sequence: 1})
	requireError(t, err, 409, "FEED_GENERATION_UNKNOWN")
	if accepted {
		t.Fatal("unknown generation retried")
	}
	if applied, err := s.Complete(ctx, batch, json.RawMessage(`{"items":[]}`), 0); err != nil || applied {
		t.Fatal(applied, err)
	}
	if err := s.DB.Exec(`UPDATE integration_agents SET next_poll_at=0 WHERE agent_id=1`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec(`UPDATE integration_feed_batches SET created_at=created_at-3600000 WHERE agent_id=1`).Error; err != nil {
		t.Fatal(err)
	}
	batch, _, err = s.Begin(ctx, 1, Request{Sequence: 2})
	if err != nil {
		t.Fatal(err)
	}
	if applied, err := s.Complete(ctx, batch, json.RawMessage(`{"items":[]}`), 0); err != nil || !applied {
		t.Fatal(applied, err)
	}
	if err := s.DB.Exec(`UPDATE integration_agents SET enabled=false WHERE agent_id=1`).Error; err != nil {
		t.Fatal(err)
	}
	_, err = s.Latest(ctx, 1)
	requireError(t, err, 403, "INTEGRATION_DISABLED")
	_, _, err = s.Begin(ctx, 1, Request{Sequence: 2})
	requireError(t, err, 403, "INTEGRATION_DISABLED")
	if err := s.DB.Exec(`DROP TABLE integration_control`).Error; err != nil {
		t.Fatal(err)
	}
	_, err = s.Latest(ctx, 1)
	requireError(t, err, 503, "INTEGRATION_FEED_UNAVAILABLE")
}

func TestPostgresRaisedMinimumAppliesToCurrentAdmission(t *testing.T) {
	s := feedStore(t)
	ctx := context.Background()
	batch, _, err := s.Begin(ctx, 1, Request{Sequence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if applied, err := s.Complete(ctx, batch, json.RawMessage(`{"items":[]}`), 0); err != nil || !applied {
		t.Fatal(applied, err)
	}
	if err := s.DB.Exec(`UPDATE integration_feed_batches SET created_at=created_at-3600001`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec(`UPDATE integration_agents SET next_poll_at=0`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec(`UPDATE integration_apps SET min_poll_interval_seconds=7200`).Error; err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Begin(ctx, 1, Request{Sequence: 2})
	requireError(t, err, 429, "FEED_POLL_TOO_EARLY")
}
