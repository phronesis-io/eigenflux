package consumer

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

type FollowupLabel struct {
	ID           int64  `gorm:"primaryKey"`
	AgentID      int64  `gorm:"not null"`
	ItemID       int64  `gorm:"not null"`
	Kind         string `gorm:"size:16;not null"`
	ImpressionID string `gorm:"size:64;not null;default:''"`
	Brief        string `gorm:"type:text;not null;default:''"`
	SessionKey   string `gorm:"size:128;not null;default:''"`
	Channel      string `gorm:"size:64;not null;default:''"`
	ServerID     string `gorm:"size:64;not null;default:''"`
	DedupKey     string `gorm:"size:32;not null"`
	ReportedAt   int64  `gorm:"not null"`
	CreatedAt    int64  `gorm:"not null"`
}

func (FollowupLabel) TableName() string { return "followup_labels" }

func batchInsertFollowupLabels(tx *gorm.DB, rows []FollowupLabel) error {
	if len(rows) == 0 {
		return nil
	}
	const batchSize = 100
	for i := 0; i < len(rows); i += batchSize {
		end := i + batchSize
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[i:end]
		cols := "(id, agent_id, item_id, kind, impression_id, brief, session_key, channel, server_id, dedup_key, reported_at, created_at)"
		placeholders := make([]string, 0, len(batch))
		args := make([]interface{}, 0, len(batch)*12)
		for _, r := range batch {
			placeholders = append(placeholders, "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
			args = append(args, r.ID, r.AgentID, r.ItemID, r.Kind, r.ImpressionID,
				r.Brief, r.SessionKey, r.Channel, r.ServerID, r.DedupKey, r.ReportedAt, r.CreatedAt)
		}
		sql := fmt.Sprintf("INSERT INTO followup_labels %s VALUES %s ON CONFLICT (dedup_key) DO NOTHING", cols, strings.Join(placeholders, ", "))
		if err := tx.Exec(sql, args...).Error; err != nil {
			return err
		}
	}
	return nil
}
