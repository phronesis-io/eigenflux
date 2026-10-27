package consumer

import (
	"time"

	"eigenflux_server/pkg/itemstats"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type FeedbackLog struct {
	ClientKey        string `gorm:"size:64;not null;default:''"`
	Surfaced         *bool
	ImpressionSource string `gorm:"size:16;not null;default:''"`
	ContractVersion  string `gorm:"size:32;not null;default:''"`
	ID               int64  `gorm:"primaryKey;autoIncrement"`
	StreamMessageID  string `gorm:"size:64;not null;uniqueIndex"`
	ImpressionID     string `gorm:"size:64;not null;default:''"`
	AgentID          int64  `gorm:"not null"`
	ItemID           int64  `gorm:"not null"`
	Score            int16  `gorm:"not null"`
	FeedbackAt       int64  `gorm:"not null"`
	CreatedAt        int64  `gorm:"not null"`
}

func (FeedbackLog) TableName() string { return "feedback_logs" }

func newFeedbackLog(msgID string, event itemstats.Event, now int64) FeedbackLog {
	return FeedbackLog{
		StreamMessageID: msgID,
		ClientKey:       event.ClientKey, Surfaced: event.Surfaced, ImpressionSource: event.ImpressionSource, ContractVersion: event.ContractVersion,
		ImpressionID: event.ImpressionID,
		AgentID:      event.AgentID,
		ItemID:       event.ItemID,
		Score:        int16(event.Score),
		FeedbackAt:   now,
		CreatedAt:    now,
	}
}

func insertFeedbackLog(tx *gorm.DB, log FeedbackLog) (bool, error) {
	result := tx.Clauses(clause.OnConflict{
		DoNothing: true,
	}).Create(&log)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func nowFeedbackLogMs() int64 {
	return time.Now().UnixMilli()
}
