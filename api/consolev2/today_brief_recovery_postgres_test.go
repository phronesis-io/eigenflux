package consolev2

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type timeoutBriefGenerator struct{ calls atomic.Int32 }

func (g *timeoutBriefGenerator) Generate(context.Context, todayBriefFacts, string) (string, error) {
	g.calls.Add(1)
	return "", context.DeadlineExceeded
}

func TestPostgresTodayBriefRecovery(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("requires migrated local Postgres")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || (cfg.Host != "127.0.0.1" && cfg.Host != "localhost") {
		t.Fatal("requires loopback database")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if err := db.Exec(query, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	id := now.UnixNano()
	exec("INSERT INTO agents(agent_id,email,agent_name,bio,created_at,updated_at,identity_state) VALUES(?,?,'Brief recovery','',?,?,'active')", id, fmt.Sprintf("brief-%d@example.test", id), now.UnixMilli(), now.UnixMilli())
	defer db.Exec("DELETE FROM agents WHERE agent_id=?", id)
	day := now.Format("2006-01-02")
	start := now.Truncate(24 * time.Hour).UnixMilli()
	exec(`INSERT INTO agent_today_model_briefs(agent_id,language,day,facts_hash,narrative,status,generated_at,updated_at)
 VALUES(?,'en',?,repeat('0',64),'Earlier brief.','ready',?,?)`, id, day, now.UnixMilli(), now.UnixMilli())
	g := &timeoutBriefGenerator{}
	svc := &Service{db: db, todayBriefGenerator: g, todayBriefSlots: make(chan struct{}, 4)}
	facts := todayBriefFacts{Day: day, FocusCount: 1}
	prepare := func() map[string]interface{} { return svc.prepareTodayBrief(id, facts, "en", time.Now().UTC(), start) }
	waitFailed := func() {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			var status string
			if err := db.Raw("SELECT status FROM agent_today_model_briefs WHERE agent_id=?", id).Scan(&status).Error; err != nil {
				t.Fatal(err)
			}
			if status == "failed" {
				return
			}
			time.Sleep(time.Millisecond * 5)
		}
		t.Fatal("timeout waiting for failed attempt")
	}
	view := prepare()
	if view["text"] != "Earlier brief." || view["stale"] != true {
		t.Fatal(view)
	}
	waitFailed()
	view = prepare()
	if view["text"] != "Earlier brief." || view["retry_at"] == nil {
		t.Fatal(view)
	}
	exec("UPDATE agent_today_model_briefs SET last_attempt_at=last_attempt_at-61000,lease_until=lease_until-61000 WHERE agent_id=?", id)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-gate; prepare() }()
	}
	close(gate)
	wg.Wait()
	waitFailed()
	view = prepare()
	if g.calls.Load() != 2 || view["retry_at"] != nil || view["text"] != "Earlier brief." {
		t.Fatalf("after concurrent retry: %v calls=%d", view, g.calls.Load())
	}
	// A pre-upgrade failed row may retain yesterday's text despite having today's day key.
	exec("UPDATE agent_today_model_briefs SET generated_at=? WHERE agent_id=?", start-1, id)
	if view = prepare(); view["text"] != nil {
		t.Fatal(view)
	}
}
