package dal

import (
	"context"
	"time"

	"eigenflux_server/pkg/recordsearch"

	"gorm.io/gorm"
)

// MatchAgentsByName returns a bounded public identity projection for matching
// counterparties. The order service still enforces participant ownership.
func MatchAgentsByName(ctx context.Context, db *gorm.DB, query string) ([]int64, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	pattern, _ := recordsearch.Literal(query)
	ids := []int64{}
	err := db.WithContext(ctx).Raw(`SELECT agent_id FROM agents WHERE agent_name ILIKE ? ESCAPE '!' OR agent_name_en ILIKE ? ESCAPE '!' OR short_id = ? ORDER BY agent_id DESC LIMIT 1001`, pattern, pattern, query).Scan(&ids).Error
	if err != nil {
		return nil, false, err
	}
	more := len(ids) > 1000
	if more {
		ids = ids[:1000]
	}
	return ids, more, nil
}
