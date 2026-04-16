package consumer

import (
	"time"

	sortDal "eigenflux_server/rpc/sort/dal"
)

const (
	simThresholdAlert = 0.85
	alertTimeWindow   = 6 * time.Hour
)

// resolveGroupID applies broadcast_type-specific rules to correct the default
// group_id assigned during the initial (info-mode) vector dedup.
// It only ungroups (returns itemID) — never reassigns to a different group.
func resolveGroupID(itemID, authorAgentID int64, broadcastType string, similarItems []sortDal.Item) int64 {
	if len(similarItems) == 0 {
		return itemID
	}

	best := similarItems[0]

	switch broadcastType {
	case "demand", "supply":
		if best.AuthorAgentID != authorAgentID || best.AuthorAgentID == 0 {
			return itemID
		}
		return best.GroupID

	case "alert":
		cutoff := time.Now().Add(-alertTimeWindow)
		if best.Score < simThresholdAlert || best.CreatedAt.Before(cutoff) {
			return itemID
		}
		return best.GroupID

	default:
		return best.GroupID
	}
}
