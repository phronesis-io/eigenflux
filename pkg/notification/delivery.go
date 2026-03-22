package notification

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IsDelivered checks if a specific notification has been delivered to an agent.
func IsDelivered(ctx context.Context, db *gorm.DB, sourceType string, sourceID, agentID int64) (bool, error) {
	var count int64
	err := db.WithContext(ctx).
		Model(&NotificationDelivery{}).
		Where("source_type = ? AND source_id = ? AND agent_id = ?", sourceType, sourceID, agentID).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// AreDelivered checks delivery state for multiple source IDs of the same source type.
// Returns a set of source_ids that have been delivered.
func AreDelivered(ctx context.Context, db *gorm.DB, sourceType string, sourceIDs []int64, agentID int64) (map[int64]bool, error) {
	if len(sourceIDs) == 0 {
		return map[int64]bool{}, nil
	}
	var rows []NotificationDelivery
	err := db.WithContext(ctx).
		Select("source_id").
		Where("source_type = ? AND source_id IN ? AND agent_id = ?", sourceType, sourceIDs, agentID).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	delivered := make(map[int64]bool, len(rows))
	for _, r := range rows {
		delivered[r.SourceID] = true
	}
	return delivered, nil
}

// RecordDelivery inserts a delivery row, ignoring conflicts (idempotent).
func RecordDelivery(ctx context.Context, db *gorm.DB, sourceType string, sourceID, agentID int64) error {
	row := &NotificationDelivery{
		SourceType:  sourceType,
		SourceID:    sourceID,
		AgentID:     agentID,
		DeliveredAt: time.Now().UnixMilli(),
	}
	return db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(row).Error
}

// RecordDeliveries batch-inserts delivery rows, ignoring conflicts.
func RecordDeliveries(ctx context.Context, db *gorm.DB, items []NotificationDelivery) error {
	if len(items) == 0 {
		return nil
	}
	return db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&items).Error
}
