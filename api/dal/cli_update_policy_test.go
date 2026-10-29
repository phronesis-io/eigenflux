package dal

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
