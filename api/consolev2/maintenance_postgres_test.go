package consolev2

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
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

	// Watch queue/attempt stages retain order and job correlation independently
	// of helper observations. Same event replay deduplicates within the actor;
	// another authenticated Agent may report the same client-side event ID.
	svc.telemetryRates = map[string]telemetryRateState{}
	for i, row := range []struct {
		stage, result, code string
		attempt             int
	}{
		{"queued", "queued", "", 0},
		{"prepare_failed", "failed", "service_name_unavailable", 1},
		{"os_submitted", "submitted", "", 2},
		{"os_failed", "failed", "permission_denied", 3},
	} {
		e := validWatchNotificationMaintenance()
		e.EventID, e.AttemptID = fmt.Sprintf("%064x", 3000+i), fmt.Sprintf("%032x", 4000+i)
		e.Notification.Stage, e.Result, e.ErrorCode = row.stage, row.result, row.code
		w := e.Notification.Watch
		w.Origin, w.SourceNotificationID, w.Attempt = "local_blocker", "", row.attempt
		w.JobID, w.JobKind, w.JobStatus, w.JobCode = strings.Repeat("e", 64), "commission_order", "unknown", "commission_intake_unconfirmed"
		body = map[string]interface{}{"events": []maintenanceEvent{e}}
		for _, id := range []int64{42, 42, 43} {
			if status := send(id); status != 202 {
				t.Fatalf("watch stage ingestion %s: %d", row.stage, status)
			}
		}
	}
	var watchRows []struct {
		AgentID                                      int64
		Stage, Result, OrderID, JobID, JobCode, Role string
		Attempt                                      int
		OrderVersion                                 int64
	}
	if err := tx.Raw(`SELECT agent_id,properties->'notification'->>'stage' AS stage,properties->>'result' AS result,
properties->'notification'->'watch'->>'order_id' AS order_id,properties->'notification'->'watch'->>'job_id' AS job_id,
properties->'notification'->'watch'->>'job_code' AS job_code,properties->'notification'->'watch'->>'role' AS role,
(properties->'notification'->'watch'->>'attempt')::integer AS attempt,
(properties->'notification'->'watch'->>'order_version')::bigint AS order_version
FROM telemetry_events_v2 WHERE properties->'notification'->>'action'='watch' ORDER BY agent_id,attempt`).Scan(&watchRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(watchRows) != 8 {
		t.Fatalf("watch identity/deduplication: %+v", watchRows)
	}
	for i, row := range watchRows {
		if row.AgentID != int64(42+i/4) || row.OrderID != "123" || row.OrderVersion != 2 || row.JobID != strings.Repeat("e", 64) || row.JobCode != "commission_intake_unconfirmed" || row.Role != "seller" || row.Attempt != i%4 {
			t.Fatalf("watch correlation projection: %+v", row)
		}
	}
	// Rejection is atomic, including an otherwise-valid preceding event.
	valid := validWatchNotificationMaintenance()
	valid.EventID = fmt.Sprintf("%064x", 9000)
	invalid := validWatchNotificationMaintenance()
	invalid.EventID, invalid.Notification.Watch.OrderID = fmt.Sprintf("%064x", 9001), "00123"
	body = map[string]interface{}{"events": []maintenanceEvent{valid, invalid}}
	if status := send(42); status != 400 {
		t.Fatalf("invalid watch batch: %d", status)
	}
	var leaked int64
	if err := tx.Raw(`SELECT count(*) FROM telemetry_events_v2 WHERE properties->'notification'->>'action'='watch'`).Scan(&leaked).Error; err != nil {
		t.Fatal(err)
	}
	if leaked != 8 {
		t.Fatal("invalid watch batch was partially persisted")
	}

	// Execution diagnostics keep independent stage and retry receipts in the same
	// account-namespaced event table. A replay cannot erase an earlier failure.
	svc.telemetryRates = map[string]telemetryRateState{}
	for i, stage := range []string{"preflight", "agent", "upload_put", "retry"} {
		e := validCommissionMaintenance()
		e.EventID, e.AttemptID = fmt.Sprintf("%064x", 10000+i), fmt.Sprintf("%032x", 11000+i)
		e.Commission.Stage = stage
		if stage == "preflight" {
			e.Commission.OrderVersion, e.Result, e.ErrorCode = 0, "failed", "preflight_unavailable"
		}
		if stage == "retry" {
			e.Result, e.ErrorCode, e.Commission.NextAttemptAt = "retry_scheduled", "transport_failed", e.EventAt+60000
		}
		body = map[string]interface{}{"events": []maintenanceEvent{e}}
		for _, id := range []int64{42, 42, 43} {
			if status := send(id); status != 202 {
				t.Fatalf("commission diagnostic %s: %d", stage, status)
			}
		}
	}
	var dispatchRows []struct {
		AgentID               int64
		Stage, OrderID, JobID string
	}
	if err := tx.Raw(`SELECT agent_id, properties->'commission'->>'stage' AS stage,
properties->'commission'->>'order_id' AS order_id,properties->'commission'->>'job_id' AS job_id
FROM telemetry_events_v2 WHERE properties->>'component'='dispatch' ORDER BY agent_id,event_id`).Scan(&dispatchRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(dispatchRows) != 8 {
		t.Fatalf("commission diagnostic replay or account scope: %+v", dispatchRows)
	}
	for i, row := range dispatchRows {
		if row.AgentID != int64(42+i/4) || row.OrderID != "123" || row.JobID != strings.Repeat("c", 64) {
			t.Fatalf("commission diagnostic correlation: %+v", row)
		}
	}

	// Same device installation may be used by different accounts. Only status
	// checks establish the latest permission; delayed uploads retain event time.
	deviceID := strings.Repeat("d", 32)
	observed := time.Now().Add(-time.Minute).UnixMilli()
	for i, row := range []struct {
		agent              int64
		at                 int64
		result, code, name string
	}{
		{42, observed, "disabled", "permission_denied", "我的电脑"},
		{43, observed + 1, "ready", "", "另一个账号的电脑"},
		{42, observed - 1, "ready", "", "旧状态延迟上传"},
	} {
		e := validNotificationMaintenance()
		e.EventID, e.AttemptID = fmt.Sprintf("%064x", 5000+i), fmt.Sprintf("%032x", 6000+i)
		e.EventAt, e.Result, e.ErrorCode = row.at, row.result, row.code
		e.FromVersion = "0.7.7"
		e.Notification.Action, e.Notification.IDHash = "status", ""
		e.Notification.DeviceID, e.Notification.DeviceName = deviceID, row.name
		body = map[string]interface{}{"events": []maintenanceEvent{e}}
		if status := send(row.agent); status != 202 {
			t.Fatalf("device status ingestion: %d", status)
		}
	}
	h.GET("/devices", func(_ context.Context, c *app.RequestContext) { c.Set("agent_id", int64(42)) }, svc.listNotificationDevices)
	status, payload, _ := performJSON(t, h, "GET", "/devices", nil)
	if status != 200 {
		t.Fatalf("device projection failed: %d %v", status, payload)
	}
	devices := payload["data"].(map[string]interface{})["devices"].([]interface{})
	if len(devices) != 1 {
		t.Fatalf("device scope: %v", devices)
	}
	device := devices[0].(map[string]interface{})
	if device["cli_version"] != "0.7.7" || device["device_name"] != "我的电脑" || device["permission_state"] != "denied" || device["stale"] != false {
		t.Fatalf("late upload or other account replaced current permission: %v", device)
	}
	exec(`UPDATE telemetry_events_v2 SET event_at=? WHERE agent_id=42 AND properties->'notification'->>'device_id'=?`, time.Now().Add(-11*time.Minute).UnixMilli(), deviceID)
	status, payload, _ = performJSON(t, h, "GET", "/devices", nil)
	devices = payload["data"].(map[string]interface{})["devices"].([]interface{})
	device = devices[0].(map[string]interface{})
	if status != 200 || device["permission_state"] != "unknown" || device["stale"] != true {
		t.Fatalf("offline observation reported as disabled: %v", payload)
	}

	// Legacy CLI installations have no notification device events.
	exec(`CREATE TEMP TABLE agent_settings (agent_id BIGINT PRIMARY KEY, cli_version TEXT) ON COMMIT DROP`)
	exec(`INSERT INTO agent_settings VALUES (42,'0.7.7'), (43,'1.0.0')`)
	exec(`DELETE FROM telemetry_events_v2 WHERE agent_id=42 AND properties->'notification'->>'device_id'=?`, deviceID)
	status, payload, _ = performJSON(t, h, "GET", "/devices", nil)
	data := payload["data"].(map[string]interface{})
	if status != 200 || len(data["devices"].([]interface{})) != 0 || data["account_cli_version"] != "0.7.7" {
		t.Fatalf("legacy account upgrade version missing or cross-account: %v", payload)
	}
	exec(`UPDATE agent_settings SET cli_version='1.0.0' WHERE agent_id=42`)
	status, payload, _ = performJSON(t, h, "GET", "/devices", nil)
	if status != 200 || payload["data"].(map[string]interface{})["account_cli_version"] != "1.0.0" {
		t.Fatalf("upgraded account version not refreshed: %v", payload)
	}

	exec(`UPDATE agent_credential_sessions SET scopes=ARRAY['feed:read'] WHERE principal_id=42`)
	if status := send(42); status != 403 {
		t.Fatalf("missing settings scope: %d", status)
	}
	status, _, _ = performJSON(t, h, "POST", "/api/v2/maintenance/events:batch", body)
	if status != 401 {
		t.Fatalf("anonymous maintenance accepted: %d", status)
	}
}
