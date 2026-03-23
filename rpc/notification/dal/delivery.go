package dal

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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

// RecordDeliveries batch-inserts delivery rows, ignoring conflicts.
func RecordDeliveries(ctx context.Context, db *gorm.DB, items []NotificationDelivery) error {
	if len(items) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	for i := range items {
		if items[i].DeliveredAt == 0 {
			items[i].DeliveredAt = now
		}
	}
	return db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&items).Error
}
