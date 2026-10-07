package agentcardapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/mq"
	profiledal "eigenflux_server/rpc/profile/dal"
)

func TestRefreshRunReqValidate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		req       RefreshRunReq
		wantErr   bool
		wantPaths string
	}{
		{"dispatched", RefreshRunReq{RunID: "run_0001", Stage: "dispatched", Trigger: "pending_line"}, false, ""},
		{"dispatched with outcome", RefreshRunReq{RunID: "run_0001", Stage: "dispatched", Outcome: "changed", Trigger: "plugin_task"}, true, ""},
		{"unchanged", RefreshRunReq{RunID: "run_0001", Stage: "completed", Outcome: "unchanged", Trigger: "plugin_task"}, false, "[]"},
		{"unchanged with paths", RefreshRunReq{RunID: "run_0001", Stage: "completed", Outcome: "unchanged", ChangedPaths: []string{"seeking"}, Trigger: "plugin_task"}, true, ""},
		{"changed dedupes", RefreshRunReq{RunID: "run_0001", Stage: "completed", Outcome: "changed", ChangedPaths: []string{"seeking", "offering", "seeking"}, Trigger: "manual_force"}, false, `["seeking","offering"]`},
		{"changed without paths", RefreshRunReq{RunID: "run_0001", Stage: "completed", Outcome: "changed", Trigger: "untracked"}, true, ""},
		{"changed unknown path", RefreshRunReq{RunID: "run_0001", Stage: "completed", Outcome: "changed", ChangedPaths: []string{"agent_id"}, Trigger: "untracked"}, true, ""},
		{"completed without outcome", RefreshRunReq{RunID: "run_0001", Stage: "completed", Trigger: "plugin_task"}, true, ""},
		{"unknown stage", RefreshRunReq{RunID: "run_0001", Stage: "started", Trigger: "plugin_task"}, true, ""},
		{"unknown trigger", RefreshRunReq{RunID: "run_0001", Stage: "dispatched", Trigger: "cron"}, true, ""},
		{"short run id", RefreshRunReq{RunID: "abc", Stage: "dispatched", Trigger: "plugin_task"}, true, ""},
		{"run id with spaces", RefreshRunReq{RunID: "run 0001 x", Stage: "dispatched", Trigger: "plugin_task"}, true, ""},
		{"long run id", RefreshRunReq{RunID: strings.Repeat("a", 65), Stage: "dispatched", Trigger: "plugin_task"}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths, err := tc.req.validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate() err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			got := ""
			if paths != nil {
				got = *paths
			}
			if got != tc.wantPaths {
				t.Fatalf("changed_paths = %q, want %q", got, tc.wantPaths)
			}
		})
	}
}

func TestPostRefreshRunRejectsInvalidBeforeQuota(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	previous := mq.RDB
	mq.RDB = rdb
	t.Cleanup(func() { mq.RDB = previous })
	h := refreshRunTestServer(42)
	for _, body := range []string{`not json`, `{"run_id":"run_0001","stage":"dispatched","trigger":"cron"}`} {
		resp := postRefreshRun(h, body, nil)
		if resp.StatusCode() != http.StatusBadRequest {
			t.Fatalf("body %q status = %d, want 400", body, resp.StatusCode())
		}
	}
	if mr.Exists("agentcard:rl:refresh-runs:42") {
		t.Fatal("invalid reports must not consume the daily quota")
	}
}

func TestPostRefreshRunPostgres(t *testing.T) {
	gdb := refreshRunTestDB(t)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	previousRDB := mq.RDB
	mq.RDB = rdb
	t.Cleanup(func() { mq.RDB = previousRDB })

	tx := refreshRunMigratedTx(t, gdb)
	previousDB := db.DB
	db.DB = tx
	t.Cleanup(func() { db.DB = previousDB })
	if err := tx.Exec(`INSERT INTO agents (agent_id) VALUES (42), (43)`).Error; err != nil {
		t.Fatal(err)
	}

	h := refreshRunTestServer(42)
	headers := map[string]string{
		"X-Client-Host":           "openclaw/1.2.3",
		"X-Client-Mode":           "plugin",
		"X-CLI-Ver":               "0.9.1",
		"X-Client-Plugin-Version": "2.0.0",
	}
	dispatch := `{"run_id":"run_0001","stage":"dispatched","trigger":"plugin_task"}`
	assertRecorded(t, postRefreshRun(h, dispatch, headers), true)
	assertRecorded(t, postRefreshRun(h, dispatch, headers), false)
	assertRecorded(t, postRefreshRun(h, `{"run_id":"run_0001","stage":"completed","outcome":"changed","changed_paths":["seeking","current_focus"],"trigger":"plugin_task"}`, headers), true)
	assertRecorded(t, postRefreshRun(h, `{"run_id":"run_0002","stage":"completed","outcome":"unchanged","trigger":"untracked"}`, map[string]string{"X-Client-Mode": "bogus"}), true)

	var rows []profiledal.ProfileRefreshRun
	if err := tx.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (duplicate dispatch must be ignored)", len(rows))
	}
	if rows[0].Stage != "dispatched" || rows[0].Outcome != nil || rows[0].ChangedPaths != nil ||
		rows[0].ClientHost != "openclaw/1.2.3" || rows[0].ClientMode != "plugin" ||
		rows[0].CLIVersion != "0.9.1" || rows[0].PluginVersion != "2.0.0" || rows[0].CreatedAt <= 0 {
		t.Fatalf("dispatched row = %+v", rows[0])
	}
	if rows[1].Outcome == nil || *rows[1].Outcome != "changed" || rows[1].ChangedPaths == nil {
		t.Fatalf("changed row = %+v", rows[1])
	}
	var paths []string
	if err := json.Unmarshal([]byte(*rows[1].ChangedPaths), &paths); err != nil || len(paths) != 2 || paths[0] != "seeking" {
		t.Fatalf("changed_paths = %v (%v)", paths, err)
	}
	if rows[2].Outcome == nil || *rows[2].Outcome != "unchanged" || rows[2].Trigger != "untracked" || rows[2].ClientMode != "" {
		t.Fatalf("unchanged row = %+v", rows[2])
	}

	// The same run_id from another agent is a different run.
	other := refreshRunTestServer(43)
	assertRecorded(t, postRefreshRun(other, dispatch, nil), true)

	// Duplicates never charge the quota: re-reminders and retries of a
	// recorded (run_id, stage) stay answerable after many repeats.
	if got := mr.Exists("agentcard:rl:refresh-runs:43"); !got {
		t.Fatal("the first new row must charge the quota")
	}
	for i := 0; i < refreshRunDailyLimit+5; i++ {
		assertRecorded(t, postRefreshRun(other, dispatch, nil), false)
	}
	// New rows exhaust the daily quota; one row was already charged above.
	for i := 1; i < refreshRunDailyLimit; i++ {
		assertRecorded(t, postRefreshRun(other, fmt.Sprintf(`{"run_id":"quota_%04d","stage":"dispatched","trigger":"pending_line"}`, i), nil), true)
	}
	if resp := postRefreshRun(other, `{"run_id":"quota_over","stage":"dispatched","trigger":"pending_line"}`, nil); resp.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status above quota = %d, want 429", resp.StatusCode())
	}
	// A duplicate is still answered after the quota is exhausted.
	assertRecorded(t, postRefreshRun(other, dispatch, nil), false)

	// Without Redis, duplicates still answer and new rows fail closed.
	mq.RDB = nil
	assertRecorded(t, postRefreshRun(other, dispatch, nil), false)
	if resp := postRefreshRun(other, `{"run_id":"no_redis_1","stage":"dispatched","trigger":"pending_line"}`, nil); resp.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("new row without Redis status = %d, want 503: %s", resp.StatusCode(), resp.Body())
	}
	mq.RDB = rdb
	if err := tx.Exec(`DELETE FROM agent_profile_refresh_runs WHERE agent_id = 43 AND run_id <> 'run_0001'`).Error; err != nil {
		t.Fatal(err)
	}

	// Retention removes only rows older than the cutoff.
	if err := tx.Exec(`UPDATE agent_profile_refresh_runs SET created_at = 1000 WHERE agent_id = 42`).Error; err != nil {
		t.Fatal(err)
	}
	deleted, saturated, err := profiledal.DeleteProfileRefreshRunsBefore(tx, time.Now().Add(-time.Hour).UnixMilli(), 2, 1)
	if err != nil || deleted != 2 || !saturated {
		t.Fatalf("first batch = (%d, %v, %v), want (2, true, nil)", deleted, saturated, err)
	}
	deleted, saturated, err = profiledal.DeleteProfileRefreshRunsBefore(tx, time.Now().Add(-time.Hour).UnixMilli(), 2, 5)
	if err != nil || deleted != 1 || saturated {
		t.Fatalf("drain = (%d, %v, %v), want (1, false, nil)", deleted, saturated, err)
	}
	var remaining int64
	if err := tx.Table("agent_profile_refresh_runs").Count(&remaining).Error; err != nil || remaining != 1 {
		t.Fatalf("remaining = %d (%v), want the recent agent 43 row", remaining, err)
	}
}

func TestRefreshRunMigrationConstraintsPostgres(t *testing.T) {
	gdb := refreshRunTestDB(t)
	tx := refreshRunMigratedTx(t, gdb)
	if err := tx.Exec(`INSERT INTO agents (agent_id) VALUES (7)`).Error; err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO agent_profile_refresh_runs (agent_id, run_id, stage, outcome, trigger, created_at) VALUES (7, 'r1', 'dispatched', 'changed', 'plugin_task', 1)`,
		`INSERT INTO agent_profile_refresh_runs (agent_id, run_id, stage, trigger, created_at) VALUES (7, 'r2', 'completed', 'plugin_task', 1)`,
		`INSERT INTO agent_profile_refresh_runs (agent_id, run_id, stage, trigger, created_at) VALUES (7, 'r3', 'dispatched', 'cron', 1)`,
		`INSERT INTO agent_profile_refresh_runs (agent_id, run_id, stage, outcome, changed_paths, trigger, created_at) VALUES (7, 'r4', 'completed', 'changed', '{}', 'plugin_task', 1)`,
		`INSERT INTO agent_profile_refresh_runs (agent_id, run_id, stage, trigger, created_at) VALUES (8, 'r5', 'dispatched', 'plugin_task', 1)`,
	} {
		if err := tx.Exec(`SAVEPOINT constraint_case`).Error; err != nil {
			t.Fatal(err)
		}
		if err := tx.Exec(statement).Error; err == nil {
			t.Fatalf("statement unexpectedly accepted: %s", statement)
		}
		if err := tx.Exec(`ROLLBACK TO SAVEPOINT constraint_case`).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Exec(`INSERT INTO agent_profile_refresh_runs (agent_id, run_id, stage, trigger, created_at) VALUES (7, 'ok', 'dispatched', 'plugin_task', 1)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(`DELETE FROM agents WHERE agent_id = 7`).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := tx.Table("agent_profile_refresh_runs").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("agent deletion must cascade, remaining=%d err=%v", count, err)
	}
}

func refreshRunTestServer(agentID int64) *server.Hertz {
	h := server.New()
	const path = "/api/v1/agents/me/card/refresh-runs"
	h.POST(path, func(_ context.Context, c *app.RequestContext) { c.Set("agent_id", agentID) }, PostRefreshRun)
	return h
}

func postRefreshRun(h *server.Hertz, body string, headers map[string]string) *protocol.Response {
	list := []ut.Header{{Key: "Content-Type", Value: "application/json"}}
	for key, value := range headers {
		list = append(list, ut.Header{Key: key, Value: value})
	}
	return ut.PerformRequest(h.Engine, http.MethodPost, "/api/v1/agents/me/card/refresh-runs",
		&ut.Body{Body: strings.NewReader(body), Len: len(body)}, list...).Result()
}

func assertRecorded(t *testing.T, resp *protocol.Response, want bool) {
	t.Helper()
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode(), resp.Body())
	}
	var payload struct {
		Code int `json:"code"`
		Data struct {
			Recorded  bool `json:"recorded"`
			Duplicate bool `json:"duplicate"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != 0 || payload.Data.Recorded != want || payload.Data.Duplicate == want {
		t.Fatalf("payload = %s, want recorded=%v", resp.Body(), want)
	}
}

func refreshRunTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for refresh-run PostgreSQL contracts")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1" {
		t.Fatal("refresh-run tests require a loopback PostgreSQL host")
	}
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return gdb
}

// refreshRunMigratedTx applies the real migration inside a private schema of
// a transaction that is always rolled back, so no state survives the test.
func refreshRunMigratedTx(t *testing.T, gdb *gorm.DB) *gorm.DB {
	t.Helper()
	raw, err := os.ReadFile("../../migrations/000114_agent_profile_refresh_runs.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(raw)
	start := strings.Index(up, "-- +goose Up")
	end := strings.Index(up, "-- +goose Down")
	if start < 0 || end < start {
		t.Fatal("migration is missing goose markers")
	}
	tx := gdb.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	for _, statement := range []string{
		`CREATE SCHEMA refresh_runs_test`,
		`SET LOCAL search_path TO refresh_runs_test`,
		`CREATE TABLE agents (agent_id BIGINT PRIMARY KEY)`,
		up[start+len("-- +goose Up") : end],
	} {
		if err := tx.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	return tx
}
