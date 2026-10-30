package migrations_test

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSlopMigrationPostgres(t *testing.T) {
	dsn := os.Getenv("SLOP_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("SLOP_PG_TEST_DSN required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" {
		t.Fatal("loopback PostgreSQL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	exec := func(q string) {
		t.Helper()
		if _, e := tx.Exec(ctx, q); e != nil {
			t.Fatal(e)
		}
	}
	exec(fmt.Sprintf("CREATE SCHEMA slop_%d; SET LOCAL search_path TO slop_%d,public", time.Now().Unix(), time.Now().Unix()))
	exec("CREATE TABLE raw_items(item_id BIGINT PRIMARY KEY); INSERT INTO raw_items VALUES (1),(2)")
	up, _, _ := strings.Cut(migration(t, "000124_item_slop_evaluations.sql"), "-- +goose Down")
	exec(up)
	exec(`INSERT INTO item_slop_evaluations(item_id,prompt_version,model,content_hash,template_hash,request_hash,response,is_slop,slop_reason,slop_score,model_quality,effective_quality,discard,blocked,evaluated_at) VALUES (1,'V0.2.1','test','c','t','r','{}',1,'S2: internal procedure.',0.9,0.8,0,false,true,1793350000000)`)
	var n int
	if err := tx.QueryRow(ctx, "SELECT sum(blocked) FROM grafana_ugc_slop_hourly").Scan(&n); err != nil || n != 1 {
		t.Fatalf("blocked: %d %v", n, err)
	}
	exec("SAVEPOINT invalid")
	if _, err := tx.Exec(ctx, "UPDATE item_slop_evaluations SET is_slop=NULL WHERE item_id=1"); err == nil {
		t.Fatal("missing label accepted")
	}
	exec("ROLLBACK TO SAVEPOINT invalid")
	if _, err := tx.Exec(ctx, "UPDATE item_slop_evaluations SET slop_score='NaN' WHERE item_id=1"); err == nil {
		t.Fatal("NaN accepted")
	}
	exec("ROLLBACK TO SAVEPOINT invalid")
	exec(`INSERT INTO item_slop_evaluations(item_id,prompt_version,model,content_hash,template_hash,request_hash,response,discard,blocked,evaluated_at) VALUES (2,'V0.2.1','test','c','t','r','{}',true,false,1793350000000)`)
	if err := tx.QueryRow(ctx, "SELECT sum(classified) FROM grafana_ugc_slop_hourly").Scan(&n); err != nil || n != 1 {
		t.Fatalf("discard excluded: %d %v", n, err)
	}
}
