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
func resolveGroupID(itemID, authorAgentID int64, broadcastType string, similarItems []sortDal.Item, now time.Time) int64 {
	if len(similarItems) == 0 {
		return itemID
	}

	best := similarItems[0]

	switch broadcastType {
	case "demand", "supply":
		// Score is not checked here — the initial search already filters at simThreshold (0.70). For demand/supply the distinguishing factor is authorship, not similarity degree.
		if best.AuthorAgentID != authorAgentID || best.AuthorAgentID == 0 {
			return itemID
		}
		return best.GroupID

	case "alert":
		cutoff := now.Add(-alertTimeWindow)
		if best.Score < simThresholdAlert || best.CreatedAt.Before(cutoff) {
			return itemID
		}
		return best.GroupID

	default:
		// info and any future/unknown types: trust the vector similarity result
		return best.GroupID
	}
}
