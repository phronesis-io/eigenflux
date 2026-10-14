package consolev2

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/jackc/pgx/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestMaintenancePostgresAgentAuthIdentityAndIdempotency(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN required for maintenance PostgreSQL HTTP test")
	}
	parsed, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "127.0.0.1" && parsed.Host != "localhost" && parsed.Host != "::1" {
		t.Fatal("loopback PostgreSQL required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	exec := func(query string, args ...interface{}) {
		t.Helper()
		if err := tx.Exec(query, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		`CREATE TEMP TABLE agents (agent_id BIGINT PRIMARY KEY, identity_state TEXT) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_principals (principal_id BIGINT PRIMARY KEY, agent_id BIGINT, status TEXT, revoked_at BIGINT) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_credential_sessions (session_id BIGINT, access_token_hash TEXT, principal_id BIGINT, audience TEXT, scopes TEXT[], revoked_at BIGINT, expires_at BIGINT, access_refresh_required BOOLEAN) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_onboarding_v2 (agent_id BIGINT PRIMARY KEY,state TEXT) ON COMMIT DROP`,
		`CREATE TEMP TABLE telemetry_events_v2 (event_id VARCHAR(128) PRIMARY KEY,agent_id BIGINT,install_session_id VARCHAR(128),console_session_id VARCHAR(128),event_type VARCHAR(64),properties JSONB,event_at BIGINT,created_at BIGINT,expires_at BIGINT) ON COMMIT DROP`,
	} {
		exec(query)
	}
	for _, id := range []int64{42, 43} {
		exec(`INSERT INTO agents VALUES (?,'active')`, id)
		exec(`INSERT INTO agent_principals VALUES (?,?,'active',NULL)`, id, id)
		exec(`INSERT INTO agent_onboarding_v2 VALUES (?,'completed')`, id)
		exec(`INSERT INTO agent_credential_sessions VALUES (?,?,?,'agent_v2',ARRAY['settings:write'],NULL,?,FALSE)`, id, hashString(fmt.Sprintf("efv2a_maintenance_%d", id)), id, time.Now().Add(time.Hour).UnixMilli())
	}
	svc := &Service{db: tx, telemetryRates: map[string]telemetryRateState{}}
	h := server.New()
	h.POST("/api/v2/maintenance/events:batch", svc.agentAuth("settings:write"), svc.recordMaintenanceBatch)
	body := map[string]interface{}{"events": []maintenanceEvent{validMaintenanceEvent()}}
	send := func(id int64) int {
		status, _, _ := performJSON(t, h, "POST", "/api/v2/maintenance/events:batch", body, ut.Header{Key: "Authorization", Value: fmt.Sprintf("Bearer efv2a_maintenance_%d", id)})
		return status
	}
	if status := send(42); status != 202 {
		t.Fatalf("first batch: %d", status)
	}
	if status := send(42); status != 202 {
		t.Fatalf("replay: %d", status)
	}
	var count int64
	tx.Table("telemetry_events_v2").Count(&count)
	if count != 1 {
		t.Fatalf("replay inserted duplicate: %d", count)
	}
	if status := send(43); status != 202 {
		t.Fatalf("other Agent: %d", status)
	}
	tx.Table("telemetry_events_v2").Count(&count)
	if count != 2 {
		t.Fatal("one Agent consumed another Agent's event ID")
	}
	var observations []struct {
		EventID   string
		AgentID   int64
		EventType string
		Result    string
		AttemptID string
	}
	if err := tx.Raw(`SELECT event_id,agent_id,event_type,properties->>'result' AS result,properties->>'attempt_id' AS attempt_id FROM telemetry_events_v2 ORDER BY agent_id`).Scan(&observations).Error; err != nil {
		t.Fatal(err)
	}
	if len(observations) != 2 || observations[0].AgentID != 42 || observations[1].AgentID != 43 || observations[0].Result != "executed" || observations[0].AttemptID != validMaintenanceEvent().AttemptID {
		t.Fatalf("identity/result projection: %+v", observations)
	}
	for _, observation := range observations {
		if telemetryIDPattern.MatchString(observation.EventID) {
			t.Fatal("browser telemetry can reserve an internal maintenance event key")
		}
	}
	uninstall := maintenanceEvent{EventID: fmt.Sprintf("%064x", 123), AttemptID: fmt.Sprintf("%032x", 456), EventAt: time.Now().UnixMilli(), Component: "cli", Trigger: "manual", Phase: "uninstall", Result: "removed", Source: "agent", Platform: "darwin", ReasonCategory: "privacy"}
	body = map[string]interface{}{"events": []maintenanceEvent{uninstall}}
	for _, id := range []int64{42, 42, 43} {
		if status := send(id); status != 202 {
			t.Fatalf("uninstall ingestion: %d", status)
		}
	}
	var uninstallCount int64
	if err := tx.Raw(`SELECT count(*) FROM telemetry_events_v2 WHERE event_type='maintenance_attempt' AND properties->>'phase'='uninstall' AND properties->>'result'='removed' AND properties->>'reason_category'='privacy'`).Scan(&uninstallCount).Error; err != nil {
		t.Fatal(err)
	}
	if uninstallCount != 2 {
		t.Fatalf("uninstall metadata or identity deduplication: %d", uninstallCount)
	}
	// Notification setup and submission are independently queryable. Replay
	// inserts no extra row and identity remains the authenticated Agent's.
	for i, result := range []string{"registered", "submitted", "failed"} {
		e := validNotificationMaintenance()
		e.EventID = fmt.Sprintf("%064x", 1000+i)
		e.AttemptID = fmt.Sprintf("%032x", 2000+i)
		e.Result = result
		if result == "registered" {
			e.Source, e.ErrorCode = "installer", "pending_first_notification"
			e.Notification.Action, e.Notification.IDHash = "setup", ""
		}
		if result == "failed" {
			e.ErrorCode = "provider_failed"
			e.Notification.Stage, e.Notification.HResult = "toast_show", "0x80070005"
		}
		body = map[string]interface{}{"events": []maintenanceEvent{e}}
		for _, id := range []int64{42, 42} {
			if status := send(id); status != 202 {
				t.Fatalf("notification ingestion: %d", status)
			}
		}
	}
	var notificationCount int64
	if err := tx.Raw(`SELECT count(*) FROM telemetry_events_v2 WHERE agent_id=42 AND event_type='maintenance_attempt' AND properties->>'phase'='notification' AND properties->'notification'->>'visibility'='unknown'`).Scan(&notificationCount).Error; err != nil {
		t.Fatal(err)
	}
	if notificationCount != 3 {
		t.Fatalf("notification storage/deduplication: %d", notificationCount)
	}
	queries, err := os.ReadFile("../../scripts/reporting/notification_statistics.sql")
	if err != nil {
		t.Fatal(err)
	}
	queryIndex := 0
	for _, query := range strings.Split(string(queries), ";") {
		if strings.TrimSpace(query) == "" {
			continue
		}
		var rows []map[string]interface{}
		if err := tx.Raw(query).Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("statistics query %d: %v", queryIndex, rows)
		}
		queryIndex++
	}
	if queryIndex != 3 {
		t.Fatal("statistics query coverage incomplete")
	}

	exec(`UPDATE agent_credential_sessions SET scopes=ARRAY['feed:read'] WHERE principal_id=42`)
	if status := send(42); status != 403 {
		t.Fatalf("missing settings scope: %d", status)
	}
	status, _, _ := performJSON(t, h, "POST", "/api/v2/maintenance/events:batch", body)
	if status != 401 {
		t.Fatalf("anonymous maintenance accepted: %d", status)
	}
}
