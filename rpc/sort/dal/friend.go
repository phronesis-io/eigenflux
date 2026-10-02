package dal

import (
	"context"
	"gorm.io/gorm"
)

// FetchFriendItems shares the bounded friend-feed recall query across ranking
// pipelines. Author selection is stable when a user has more than maxAuthors.
func FetchFriendItems(ctx context.Context, db *gorm.DB, owner int64, maxAuthors, windowHours, limit int) ([]Item, error) {
	if maxAuthors <= 0 || limit <= 0 {
		return nil, nil
	}
	var authors []int64
	if err := db.WithContext(ctx).Table("user_relations").Where("from_uid = ? AND rel_type = ?", owner, 1).
		Order("to_uid").Limit(maxAuthors).Pluck("to_uid", &authors).Error; err != nil {
		return nil, err
	}
	return FetchRecentItemsByAuthors(ctx, authors, windowHours, limit)
}
