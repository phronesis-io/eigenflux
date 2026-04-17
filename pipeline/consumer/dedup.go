package consumer

import (
	"time"

	sortDal "eigenflux_server/rpc/sort/dal"
)

const (
	simThreshold      = 0.70
	simThresholdAlert = 0.85
	alertTimeWindow   = 6 * time.Hour
)

// assignDefaultGroupID picks a group_id from the similarity search results
// using info-mode rules (first match wins). This is the safe default applied
// before broadcast_type is known.
func assignDefaultGroupID(itemID int64, similarItems []sortDal.Item) int64 {
	if len(similarItems) > 0 {
		return similarItems[0].GroupID
	}
	return itemID
}

// resolveGroupID applies broadcast_type-specific rules to correct the default
// group_id assigned during the initial (info-mode) vector dedup.
// It scans all similarItems to find the best match per type rules.
// It only ungroups (returns itemID) — never reassigns to a different group.
func resolveGroupID(itemID, authorAgentID int64, broadcastType string, similarItems []sortDal.Item, now time.Time) int64 {
	if len(similarItems) == 0 {
		return itemID
	}

	switch broadcastType {
	case "demand", "supply":
		// Only check the top match (similarItems[0]). Per spec, corrections
		// only ungroup — never reassign to a different group.
		if similarItems[0].AuthorAgentID == authorAgentID && similarItems[0].AuthorAgentID != 0 {
			return similarItems[0].GroupID
		}
		return itemID

	case "alert":
		cutoff := now.Add(-alertTimeWindow)
		if similarItems[0].Score >= simThresholdAlert && similarItems[0].CreatedAt.After(cutoff) {
			return similarItems[0].GroupID
		}
		return itemID

	default:
		// info and any future/unknown types: trust the vector similarity result
		return similarItems[0].GroupID
	}
}
