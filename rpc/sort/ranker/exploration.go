package ranker

import (
	"time"

	sortDal "eigenflux_server/rpc/sort/dal"
)

// PickExplorationItems selects items for the exploration slot.
// Criteria: recent (within maxAge), complete (has Type), quality >= minQuality, not seen.
func PickExplorationItems(candidates []sortDal.Item, seenIDs map[int64]bool, count int, maxAge time.Duration, minQuality float64) []sortDal.Item {
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

	if len(eligible) > count {
		eligible = eligible[:count]
	}
	return eligible
}
