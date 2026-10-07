package dal

import (
	"time"

	"gorm.io/gorm"
)

// ProfileRefreshRun is one append-only Periodic Profile Refresh health event.
// ChangedPaths is raw JSONB array text and is only set for completed runs.
type ProfileRefreshRun struct {
	ID            int64   `gorm:"column:id;primaryKey"`
	AgentID       int64   `gorm:"column:agent_id"`
	RunID         string  `gorm:"column:run_id"`
	Stage         string  `gorm:"column:stage"`
	Outcome       *string `gorm:"column:outcome"`
	ChangedPaths  *string `gorm:"column:changed_paths"`
	Trigger       string  `gorm:"column:trigger"`
	ClientHost    string  `gorm:"column:client_host"`
	ClientMode    string  `gorm:"column:client_mode"`
	CLIVersion    string  `gorm:"column:cli_version"`
	PluginVersion string  `gorm:"column:plugin_version"`
	CreatedAt     int64   `gorm:"column:created_at"`
}

func (ProfileRefreshRun) TableName() string { return "agent_profile_refresh_runs" }

// InsertProfileRefreshRun appends one run event. A repeated (agent, run, stage)
// is a client retry and reports inserted=false without changing the original.
func InsertProfileRefreshRun(db *gorm.DB, run *ProfileRefreshRun) (bool, error) {
	if run.CreatedAt == 0 {
		run.CreatedAt = time.Now().UnixMilli()
	}
	run.ClientHost = clampRunes(run.ClientHost, 129)
	run.ClientMode = clampRunes(run.ClientMode, 16)
	run.CLIVersion = clampRunes(run.CLIVersion, 32)
	run.PluginVersion = clampRunes(run.PluginVersion, 32)
	res := db.Exec(`INSERT INTO agent_profile_refresh_runs
			(agent_id, run_id, stage, outcome, changed_paths, trigger,
			 client_host, client_mode, cli_version, plugin_version, created_at)
		VALUES (?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (agent_id, run_id, stage) DO NOTHING`,
		run.AgentID, run.RunID, run.Stage, run.Outcome, run.ChangedPaths, run.Trigger,
		run.ClientHost, run.ClientMode, run.CLIVersion, run.PluginVersion, run.CreatedAt)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// DeleteProfileRefreshRunsBefore removes run telemetry older than the cutoff
// in bounded batches. It reports saturated=true when maxBatches were all full.
func DeleteProfileRefreshRunsBefore(db *gorm.DB, beforeCreatedAtMs int64, batchSize, maxBatches int) (int64, bool, error) {
	if batchSize <= 0 {
		batchSize = 5000
	}
	if maxBatches <= 0 {
		maxBatches = 1
	}
	var total int64
	for batch := 0; batch < maxBatches; batch++ {
		res := db.Exec(`WITH doomed AS (
			SELECT id FROM agent_profile_refresh_runs
			WHERE created_at < ?
			ORDER BY created_at, id
			LIMIT ?
		)
		DELETE FROM agent_profile_refresh_runs AS run
		USING doomed
		WHERE run.id = doomed.id`, beforeCreatedAtMs, batchSize)
		if res.Error != nil {
			return total, false, res.Error
		}
		total += res.RowsAffected
		if res.RowsAffected < int64(batchSize) {
			return total, false, nil
		}
	}
	return total, true, nil
}
