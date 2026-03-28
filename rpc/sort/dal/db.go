package dal

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// Item processing status codes (subset relevant to sort queries).
const StatusCompleted int16 = 3

// MatchItemsByKeywords finds item_ids matching any of the given keywords using ILIKE on the processed_items table's keywords column.
// Returns item_ids ordered by updated_at DESC with cursor pagination using lastUpdatedAt.
func MatchItemsByKeywords(db *gorm.DB, keywords []string, lastUpdatedAt int64, limit int) ([]int64, []int64, error) {
	if limit <= 0 {
		limit = 20
	}
	var conditions []string
	var args []interface{}
	for _, kw := range keywords {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		conditions = append(conditions, "keywords ILIKE ?")
		args = append(args, fmt.Sprintf("%%%s%%", kw))
	}
	if len(conditions) == 0 {
		return nil, nil, nil
	}

	whereClause := fmt.Sprintf("status = %d AND (", StatusCompleted) + strings.Join(conditions, " OR ") + ")"
	if lastUpdatedAt > 0 {
		whereClause += " AND updated_at < ?"
		args = append(args, lastUpdatedAt)
	}

	type result struct {
		ItemID    int64 `gorm:"column:item_id"`
		UpdatedAt int64 `gorm:"column:updated_at"`
	}
	var results []result
	err := db.Table("processed_items").
		Select("item_id, updated_at").
		Where(whereClause, args...).
		Order("updated_at DESC, item_id DESC").
		Limit(limit).
		Find(&results).Error

	var itemIDs []int64
	var updatedAts []int64
	for _, r := range results {
		itemIDs = append(itemIDs, r.ItemID)
		updatedAts = append(updatedAts, r.UpdatedAt)
	}
	return itemIDs, updatedAts, err
}

// GetLatestItemIDs returns the latest item_ids ordered by updated_at DESC (fallback when no keywords).
func GetLatestItemIDs(db *gorm.DB, lastUpdatedAt int64, limit int) ([]int64, []int64, error) {
	if limit <= 0 {
		limit = 20
	}

	type result struct {
		ItemID    int64 `gorm:"column:item_id"`
		UpdatedAt int64 `gorm:"column:updated_at"`
	}
	var results []result
	tx := db.Table("processed_items").Select("item_id, updated_at").Where("status = ?", StatusCompleted)
	if lastUpdatedAt > 0 {
		tx = tx.Where("updated_at < ?", lastUpdatedAt)
	}
	err := tx.Order("updated_at DESC, item_id DESC").Limit(limit).Find(&results).Error

	var itemIDs []int64
	var updatedAts []int64
	for _, r := range results {
		itemIDs = append(itemIDs, r.ItemID)
		updatedAts = append(updatedAts, r.UpdatedAt)
	}
	return itemIDs, updatedAts, err
}
