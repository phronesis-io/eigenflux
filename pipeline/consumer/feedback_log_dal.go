package consumer

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type FeedbackLog struct {
	ID              int64  `gorm:"primaryKey;autoIncrement"`
	StreamMessageID string `gorm:"size:64;not null;uniqueIndex"`
	ImpressionID    string `gorm:"size:64;not null;default:''"`
	AgentID         int64  `gorm:"not null"`
	ItemID          int64  `gorm:"not null"`
	Score           int16  `gorm:"not null"`
	FeedbackAt      int64  `gorm:"not null"`
	CreatedAt       int64  `gorm:"not null"`
}

func (FeedbackLog) TableName() string { return "feedback_logs" }

func newFeedbackLog(msgID string, event itemStatsEventLike, now int64) FeedbackLog {
	return FeedbackLog{
		StreamMessageID: msgID,
		ImpressionID:    event.GetImpressionID(),
		AgentID:         event.GetAgentID(),
		ItemID:          event.GetItemID(),
		Score:           int16(event.GetScore()),
		FeedbackAt:      now,
		CreatedAt:       now,
	}
}

func insertFeedbackLog(tx *gorm.DB, log FeedbackLog) (bool, error) {
	result := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "stream_message_id"}},
		DoNothing: true,
	}).Create(&log)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

type itemStatsEventLike interface {
	GetAgentID() int64
	GetItemID() int64
	GetScore() int
	GetImpressionID() string
}

type feedbackEvent struct {
	agentID      int64
	itemID       int64
	score        int
	impressionID string
}

func (e feedbackEvent) GetAgentID() int64       { return e.agentID }
func (e feedbackEvent) GetItemID() int64        { return e.itemID }
func (e feedbackEvent) GetScore() int           { return e.score }
func (e feedbackEvent) GetImpressionID() string { return e.impressionID }

func nowFeedbackLogMs() int64 {
	return time.Now().UnixMilli()
}
