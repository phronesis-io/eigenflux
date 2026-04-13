package ranker

import (
	"time"

	sortDal "eigenflux_server/rpc/sort/dal"
)

// PickExplorationItems selects items for the exploration slot.
// Criteria: recent (within maxAge), complete (has Type), quality >= minQuality,
// not already selected by item_id, and not already selected by group_id.
func PickExplorationItems(candidates []sortDal.Item, seenIDs, seenGroupIDs map[int64]bool, count int, maxAge time.Duration, minQuality float64) []sortDal.Item {
	if count <= 0 || len(candidates) == 0 {
		return nil
	}

	now := time.Now()
	cutoff := now.Add(-maxAge)

	var eligible []sortDal.Item
	for _, item := range candidates {
		if item.Type == "" {
			continue
		}
		if item.QualityScore < minQuality {
			continue
		}
		if item.UpdatedAt.Before(cutoff) {
			continue
		}
		if seenIDs[item.ID] {
			continue
		}
		if item.GroupID != 0 && seenGroupIDs[item.GroupID] {
			continue
		}
		eligible = append(eligible, item)
	}

	// Sort by quality descending (selection sort, N is small)
	for i := 0; i < len(eligible) && i < count; i++ {
		best := i
		for j := i + 1; j < len(eligible); j++ {
			if eligible[j].QualityScore > eligible[best].QualityScore {
				best = j
			}
		}
		eligible[i], eligible[best] = eligible[best], eligible[i]
	}

	result := make([]sortDal.Item, 0, count)
	selectedGroupIDs := make(map[int64]bool, len(seenGroupIDs)+count)
	for groupID, seen := range seenGroupIDs {
		if seen {
			selectedGroupIDs[groupID] = true
		}
	}
	for _, item := range eligible {
		if item.GroupID != 0 && selectedGroupIDs[item.GroupID] {
			continue
		}
		if item.GroupID != 0 {
			selectedGroupIDs[item.GroupID] = true
		}
		result = append(result, item)
		if len(result) >= count {
			break
		}
	}
	return result
}
