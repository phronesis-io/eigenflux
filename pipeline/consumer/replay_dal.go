package consumer

import (
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

type ReplayLog struct {
	ID            int64  `gorm:"primaryKey"`
	ImpressionID  string `gorm:"size:64;not null"`
	AgentID       int64  `gorm:"not null"`
	ItemID        int64  `gorm:"not null"`
	AgentFeatures string `gorm:"type:jsonb;not null;default:'{}'"`
	ItemFeatures  string `gorm:"type:jsonb;not null;default:'{}'"`
	ItemScore     *float64
	Position      int   `gorm:"not null;default:0"`
	ServedAt      int64 `gorm:"not null"`
	CreatedAt     int64 `gorm:"not null"`
	// Delivered is nil when the event predates the delivered flag (rolling
	// deploy); such rows are excluded from beat-coverage "pushed" counts.
	Delivered *bool
}

func (ReplayLog) TableName() string { return "replay_logs" }

func batchInsertReplayLogs(tx *gorm.DB, logs []ReplayLog) error {
	if len(logs) == 0 {
		return nil
	}

	const batchSize = 100
	for i := 0; i < len(logs); i += batchSize {
		end := i + batchSize
		if end > len(logs) {
			end = len(logs)
		}
		batch := logs[i:end]

		cols := "(id, impression_id, agent_id, item_id, agent_features, item_features, item_score, position, served_at, created_at, delivered)"
		placeholders := make([]string, 0, len(batch))
		args := make([]interface{}, 0, len(batch)*11)
		for _, l := range batch {
			placeholders = append(placeholders, "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
			args = append(args, l.ID, l.ImpressionID, l.AgentID, l.ItemID,
				l.AgentFeatures, l.ItemFeatures, l.ItemScore, l.Position,
				l.ServedAt, l.CreatedAt, l.Delivered)
		}

		sql := fmt.Sprintf("INSERT INTO replay_logs %s VALUES %s ON CONFLICT (impression_id, position) DO NOTHING", cols, strings.Join(placeholders, ", "))
		if err := tx.Exec(sql, args...).Error; err != nil {
			return err
		}
	}
	return nil
}

// DeleteOldReplayLogs purges replay_logs rows whose served_at is older than
// beforeServedAtMs. replay_logs is append-only and high-volume, so deletes run
// in batches (bounded by ctid) to avoid a single long-held lock on the table.
// It returns the total number of rows deleted.
func DeleteOldReplayLogs(db *gorm.DB, beforeServedAtMs int64, batchSize int) (int64, error) {
	if batchSize <= 0 {
		batchSize = 5000
	}
	var total int64
	for {
		res := db.Exec(
			"DELETE FROM replay_logs WHERE ctid IN (SELECT ctid FROM replay_logs WHERE served_at < ? LIMIT ?)",
			beforeServedAtMs, batchSize,
		)
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
		if res.RowsAffected < int64(batchSize) {
			return total, nil
		}
	}
}

func nowMs() int64 {
	return time.Now().UnixMilli()
}
