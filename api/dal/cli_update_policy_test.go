package dal

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestCLIUpdatePolicyObservationsPreserveUnknownAndActor(t *testing.T) {
	db := observationDB(t)
	require.NoError(t, db.Table("telemetry_events_v2").AutoMigrate(&cliUpdatePolicyEvent{}))
	at := time.Now().UnixMilli()
	require.NoError(t, ReportCLIUpdatePolicy(db, 41, at, nil, "old", "codex", "skill"))
	var count int64
	require.NoError(t, db.Table("telemetry_events_v2").Count(&count).Error)
	require.Zero(t, count)
	for _, enabled := range []bool{true, false} {
		id := int64(42)
		if !enabled {
			id = 43
		}
		for i := 0; i < 2; i++ {
			require.NoError(t, ReportCLIUpdatePolicy(db, id, at, &enabled, "0.7.6", "codex", "skill"))
		}
		var row cliUpdatePolicyEvent
		require.NoError(t, db.Table("telemetry_events_v2").Where("agent_id = ?", id).First(&row).Error)
		var props map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(row.Properties), &props))
		require.Equal(t, enabled, props["auto_cli_update"])
		require.Equal(t, "0.7.6", props["cli_version"])
		require.Equal(t, "cli_update_policy", row.EventType)
		require.Equal(t, at, row.EventAt)
		require.Equal(t, int64((30*24*time.Hour)/time.Millisecond), row.ExpiresAt-row.CreatedAt)
	}
	require.NoError(t, db.Table("telemetry_events_v2").Count(&count).Error)
	require.Equal(t, int64(2), count)
	var settings AgentSettings
	require.NoError(t, db.Model(&settings).Count(&count).Error)
	require.Zero(t, count, "policy observations must not write control settings")
}

func TestCLIUpdatePolicyPostgresJSONB(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN required for PostgreSQL policy observations")
	}
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"127.0.0.1", "localhost", "::1"}, config.Host)
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	tx := database.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	require.NoError(t, tx.Exec(`CREATE TEMP TABLE telemetry_events_v2 (
event_id VARCHAR(128) PRIMARY KEY,agent_id BIGINT,event_type VARCHAR(64),
properties JSONB,event_at BIGINT,created_at BIGINT,expires_at BIGINT) ON COMMIT DROP`).Error)
	at := time.Now().UnixMilli()
	require.NoError(t, ReportCLIUpdatePolicy(tx, 41, at, nil, "", "", ""))
	for i, enabled := range []bool{true, false} {
		for retry := 0; retry < 2; retry++ {
			require.NoError(t, ReportCLIUpdatePolicy(tx, int64(42+i), at, &enabled, "0.7.6", "codex", "skill"))
		}
	}
	var count int64
	require.NoError(t, tx.Raw(`SELECT count(*) FROM telemetry_events_v2 WHERE
event_type='cli_update_policy' AND jsonb_typeof(properties->'auto_cli_update')='boolean'`).Scan(&count).Error)
	require.Equal(t, int64(2), count)
	require.NoError(t, tx.Raw(`SELECT count(*) FROM telemetry_events_v2 WHERE
properties->>'auto_cli_update'='false'`).Scan(&count).Error)
	require.Equal(t, int64(1), count)
}
