package recommendationmetrics

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/lib/pq"
)

func fixtureDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("RECOMMENDATION_METRICS_TEST_DSN")
	if dsn == "" {
		t.Skip("set RECOMMENDATION_METRICS_TEST_DSN to an isolated loopback recommendation_metrics_* database")
	}
	u, err := url.Parse(dsn)
	if err != nil || !strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "recommendation_metrics_") ||
		(u.Hostname() != "localhost" && (net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback())) {
		t.Fatal("recommendation metrics fixtures require an explicit isolated loopback database")
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("effect_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	conn, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn.Close()
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	_, err = conn.Exec(`
	 CREATE TABLE agents(agent_id bigint PRIMARY KEY,email text,is_official boolean);
	 CREATE TABLE raw_items(item_id bigint PRIMARY KEY,author_agent_id bigint);
	 CREATE TABLE replay_logs(id bigint PRIMARY KEY,agent_id bigint,item_id bigint,
	 impression_id text,served_at bigint,delivered boolean,source_kind text,
	 pipeline_version text,sample_schema_version integer,request_mode text,need_id bigint,item_features jsonb);
	 CREATE TABLE feedback_logs(id bigserial,agent_id bigint,item_id bigint,impression_id text,feedback_at bigint,score integer);
	 CREATE TABLE followup_labels(agent_id bigint,item_id bigint,impression_id text,reported_at bigint,kind text);
	 INSERT INTO agents VALUES (1,'consumer@example.invalid',false),(2,'second@example.invalid',false),
	 (3,'source@pgc.eigenflux.one',false),(4,'author@example.invalid',false),
	 (5,'official@example.invalid',true),(6,'bot@bot.eigenflux.one',false);
	 INSERT INTO raw_items VALUES (10,3),(20,4),(30,5),(40,6),(50,999);
	 `)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000113_recommendation_effect_daily.sql"))
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	if _, err := conn.Exec(up); err != nil {
		t.Fatal(err)
	}
	return conn
}

func instant(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return t
}

func TestPostgresRecommendationAttributionAndMaturity(t *testing.T) {
	c := fixtureDB(t)
	ctx := context.Background()
	served := instant("2026-10-02T12:00:00+08:00").UnixMilli()
	// A mixed batch contains both Profile and Need. Malformed metadata,
	// duplicate positions, searches and undelivered candidates stay separate.
	_, err := c.Exec(`INSERT INTO replay_logs
	 SELECT id,agent,item,imp,$1::bigint+offset_ms,delivered,kind,version,2,mode,need,
	 jsonb_build_object('search',jsonb_build_object('context',jsonb_build_object('input_origin',origin,'source_need_id',need::text)))
	 FROM (VALUES
	 (1,1,10,'mixed',0,true,'broadcast','need_search_v1','recommendation',0,'agent_context'),
	 (2,1,20,'mixed',0,true,'broadcast','need_search_v1','recommendation',55,'need_input'),
	 (3,1,10,'search',0,true,'broadcast','need_search_v1','search',0,'query'),
	 (4,2,20,'wrong-agent',0,true,'broadcast','need_search_v1','recommendation',0,'agent_context'),
	 (5,1,10,'not-delivered',0,false,'broadcast','need_search_v1','recommendation',0,'agent_context'),
	 (6,1,10,'legacy',0,true,'broadcast','legacy_feed_v1','feed',0,'agent_context'),
	 (7,1,20,'invalid-need',0,true,'broadcast','need_search_v1','recommendation',0,'need_input'),
	 (8,1,30,'official',0,true,'broadcast','need_search_v1','recommendation',0,'agent_context'),
	 (9,1,10,'ambiguous',0,true,'broadcast','need_search_v1','recommendation',0,'agent_context'),
	 (10,1,10,'ambiguous',0,true,'broadcast','need_search_v1','recommendation',55,'need_input'),
	 (11,1,10,'',0,true,'broadcast','need_search_v1','recommendation',0,'agent_context'),
	 (12,1,NULL,'typed',0,true,'commission','need_search_v1','recommendation',0,'agent_context'),
	 (13,5,10,'internal',0,true,'broadcast','need_search_v1','recommendation',0,'agent_context'),
	 (14,1,40,'bot-content',0,true,'broadcast','need_search_v1','recommendation',0,'agent_context'),
	 (15,1,50,'missing-author',0,true,'broadcast','need_search_v1','recommendation',0,'agent_context'),
	 (16,1,20,'midnight',43200000,true,'broadcast','need_search_v1','recommendation',0,'agent_context')
	 ) f(id,agent,item,imp,offset_ms,delivered,kind,version,mode,need,origin)`, served)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Exec(`INSERT INTO feedback_logs(agent_id,item_id,impression_id,feedback_at,score)
	 SELECT agent,item,imp,$1::bigint+offset_ms,score FROM (VALUES
	 (1,10,'mixed',1000,1),(1,10,'mixed',2000,0),(1,10,'mixed',169200000,-1),
	 (1,10,'mixed',172800000,2),(1,20,'mixed',3000,2),
	 (1,10,'search',1000,2),(1,20,'wrong-agent',1000,1),
	 (1,10,'not-delivered',1000,1),(1,10,'ambiguous',1000,1),
	 (1,10,'',1000,1),(1,10,'missing',1000,-1),(1,10,'mixed',1000,7),
	 (5,10,'internal',1000,1),(1,10,'mixed',-1000,2),
	 (1,10,'typed',1000,1)
	 ) f(agent,item,imp,offset_ms,score)`, served)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(`INSERT INTO followup_labels VALUES (1,20,'mixed',$1::bigint+4000,'surface'),
	 (1,20,'mixed',$1::bigint+5000,'question'),(1,20,'missing',$1::bigint+5000,'task')`, served); err != nil {
		t.Fatal(err)
	}
	cutoff := instant("2026-10-04T12:00:00+08:00")
	if err = RefreshDay(ctx, c, "2026-10-02", cutoff); err != nil {
		t.Fatal(err)
	}
	var deliveries, unidentifiable, events, positive, zero, mature, scored, matureEvents, negative, strong int64
	err = c.QueryRow(`SELECT delivery_rows,unidentifiable_deliveries,feedback_events,score_1,score_0,
	 mature_exposures,mature_scored_exposures,mature_feedback_events,mature_score_neg1,mature_score_2
	 FROM recommendation_effect_daily WHERE day='2026-10-02' AND basis='profile' AND lane='pgc'`).Scan(
		&deliveries, &unidentifiable, &events, &positive, &zero, &mature, &scored, &matureEvents, &negative, &strong)
	if err != nil {
		t.Fatal(err)
	}
	if deliveries != 3 || unidentifiable != 2 || events != 2 || positive != 1 || zero != 1 || mature != 1 || scored != 1 || matureEvents != 3 || negative != 1 || strong != 0 {
		t.Fatalf("profile PGC counts: %v", []int64{deliveries, unidentifiable, events, positive, zero, mature, scored, matureEvents, negative, strong})
	}
	for _, row := range []struct {
		basis, lane, column string
		want                int64
	}{
		{"need", "ugc", "feedback_events", 1}, {"need", "ugc", "score_2", 1},
		{"search", "pgc", "feedback_events", 1}, {"search", "pgc", "delivery_rows", 0},
		{"unattributed", "pgc", "feedback_events", 6}, {"unattributed", "ugc", "feedback_events", 1},
		{"unknown", "pgc", "delivery_rows", 1}, {"unknown", "ugc", "delivery_rows", 1},
		{"profile", "official", "delivery_rows", 2}, {"profile", "unknown", "delivery_rows", 1},
		{"need", "ugc", "surface_events", 1}, {"need", "ugc", "question_events", 1},
		{"unattributed", "ugc", "task_events", 1},
	} {
		var got int64
		if err := c.QueryRow("SELECT "+row.column+" FROM recommendation_effect_daily WHERE day='2026-10-02' AND basis=$1 AND lane=$2", row.basis, row.lane).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != row.want {
			t.Errorf("%s/%s/%s=%d want %d", row.basis, row.lane, row.column, got, row.want)
		}
	}
	// Repeated, older and interrupted snapshots must not partially replace a day.
	if err := RefreshDay(ctx, c, "2026-10-02", cutoff); err != nil {
		t.Fatal(err)
	}
	if err := RefreshDay(ctx, c, "2026-10-02", cutoff.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	var count int
	var minCutoff time.Time
	if err := c.QueryRow(`SELECT count(*),min(snapshot_at) FROM recommendation_effect_daily WHERE day='2026-10-02'`).Scan(&count, &minCutoff); err != nil || count != 28 || !minCutoff.Equal(cutoff) {
		t.Fatalf("incomplete or regressed day: count=%d cutoff=%v err=%v", count, minCutoff, err)
	}
	if err := RefreshDay(ctx, c, "2026-10-03", cutoff); err != nil {
		t.Fatal(err)
	}
	if err := c.QueryRow(`SELECT delivery_rows,mature_exposures FROM recommendation_effect_daily WHERE day='2026-10-03' AND basis='profile' AND lane='ugc'`).Scan(&deliveries, &mature); err != nil || deliveries != 1 || mature != 0 {
		t.Fatalf("Shanghai midnight / immature cohort counts: %d,%d err=%v", deliveries, mature, err)
	}
	days, err := PendingDays(ctx, c, cutoff)
	if err != nil || len(days) != 6 || days[0] != "2026-10-04" {
		t.Fatalf("bounded priority: %v %v", days, err)
	}
	_, err = c.Exec(`CREATE FUNCTION reject_refresh() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
	 IF NEW.basis='unattributed' THEN RAISE EXCEPTION 'fixture interruption'; END IF; RETURN NEW; END $$;
	 CREATE TRIGGER reject_refresh BEFORE INSERT ON recommendation_effect_daily FOR EACH ROW EXECUTE FUNCTION reject_refresh();`)
	if err != nil {
		t.Fatal(err)
	}
	if err := RefreshDay(ctx, c, "2026-10-02", cutoff.Add(time.Hour)); err == nil {
		t.Fatal("interrupted day reported success")
	}
	if err := c.QueryRow(`SELECT min(snapshot_at) FROM recommendation_effect_daily WHERE day='2026-10-02'`).Scan(&minCutoff); err != nil || !minCutoff.Equal(cutoff) {
		t.Fatalf("interruption committed partial data: %v %v", minCutoff, err)
	}
}

func TestPostgresRecommendationDayLock(t *testing.T) {
	c := fixtureDB(t)
	tx, err := c.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("SELECT pg_advisory_xact_lock(113,20261002)"); err != nil {
		t.Fatal(err)
	}
	err = RefreshDay(context.Background(), c, "2026-10-02", instant("2026-10-04T12:00:00+08:00"))
	if err == nil || !strings.Contains(err.Error(), "already refreshing") {
		t.Fatalf("competing connection did not respect transaction lock: %v", err)
	}
}

func TestPostgresRecommendationRepresentativeDay(t *testing.T) {
	c := fixtureDB(t)
	served := instant("2026-10-02T12:00:00+08:00").UnixMilli()
	_, err := c.Exec(`
	 CREATE INDEX ON replay_logs(served_at);
	 CREATE INDEX ON replay_logs(impression_id);
	 CREATE INDEX ON feedback_logs(agent_id,feedback_at);
	 CREATE INDEX ON feedback_logs(impression_id);
	 INSERT INTO agents SELECT n,'fixture@example.invalid',false FROM generate_series(100,599) n;`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Exec(`INSERT INTO replay_logs
	 SELECT n,100+n%500,CASE WHEN n%2=0 THEN 10 ELSE 20 END,'batch-'||n,$1::bigint+n,
	 true,'broadcast','need_search_v1',2,'recommendation',CASE WHEN n%2=0 THEN 55 ELSE 0 END,
	 jsonb_build_object('search',jsonb_build_object('context',jsonb_build_object(
	 'input_origin',CASE WHEN n%2=0 THEN 'need_input' ELSE 'agent_context' END,
	 'source_need_id',CASE WHEN n%2=0 THEN '55' ELSE '0' END)))
	 FROM generate_series(1,50000) n;`, served)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Exec(`INSERT INTO feedback_logs(agent_id,item_id,impression_id,feedback_at,score)
	 SELECT agent_id,item_id,impression_id,served_at+1000,id%4-1 FROM replay_logs WHERE id<=20000`)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := RefreshDay(context.Background(), c, "2026-10-02", instant("2026-10-05T12:00:00+08:00")); err != nil {
		t.Fatal(err)
	}
	t.Logf("50,000 deliveries / 20,000 scores refreshed in %s", time.Since(started))
	var rows, exposures, covered, events int64
	err = c.QueryRow(`SELECT sum(delivery_rows),sum(mature_exposures),sum(mature_scored_exposures),sum(mature_feedback_events)
	 FROM recommendation_effect_daily WHERE day='2026-10-02'`).Scan(&rows, &exposures, &covered, &events)
	if err != nil || rows != 50000 || exposures != 50000 || covered != 20000 || events != 20000 {
		t.Fatalf("representative day counts = %d,%d,%d,%d; err=%v", rows, exposures, covered, events, err)
	}
}
