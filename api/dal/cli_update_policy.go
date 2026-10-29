package dal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type cliUpdatePolicyEvent struct {
	EventID    string `gorm:"primaryKey"`
	AgentID    int64
	EventType  string
	Properties string `gorm:"type:jsonb"`
	EventAt    int64
	CreatedAt  int64
	ExpiresAt  int64
}

// ReportCLIUpdatePolicy records client observations without changing settings.
// Missing observations stay unknown; they never inherit a server default.
func ReportCLIUpdatePolicy(tx *gorm.DB, agentID, observedAt int64, enabled *bool, cliVersion, host, mode string) error {
	if enabled == nil {
		return nil
	}
	properties, err := json.Marshal(map[string]interface{}{
		"auto_cli_update": *enabled,
		"cli_version":     boundedObservationText(cliVersion, 32),
		"host":            boundedObservationText(host, 129),
		"mode":            mode,
	})
	if err != nil {
		return err
	}
	key := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%d\x00%s", agentID, observedAt, properties)))
	now := time.Now()
	row := cliUpdatePolicyEvent{
		EventID: "cli-policy:" + hex.EncodeToString(key[:]), AgentID: agentID,
		EventType: "cli_update_policy", Properties: string(properties), EventAt: observedAt,
		CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(30 * 24 * time.Hour).UnixMilli(),
	}
	return tx.Table("telemetry_events_v2").Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "event_id"}}, DoNothing: true,
	}).Create(&row).Error
}
