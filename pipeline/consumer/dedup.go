package consumer

import (
	"time"

	itemDal "eigenflux_server/rpc/item/dal"
	sortDal "eigenflux_server/rpc/sort/dal"
)

const (
	// simThreshold gates default (info-mode) grouping by kNN cosine. Raised
	// 0.70 -> 0.80: at 0.70 same-author boilerplate ("人设" preamble) lifted
	// cross-topic posts into one band (~0.69-0.78), causing single-linkage
	// drift that merged distinct topics. 0.80 keeps near-duplicates grouped
	// (dupes sit at ~0.99) while letting different topics form their own group.
	simThreshold      = 0.80
	simThresholdAlert = 0.85
	alertTimeWindow   = 6 * time.Hour
)

// exactDuplicateDecision is the outcome of an exact content-hash collision.
type exactDuplicateDecision struct {
	Discard     bool
	DuplicateOf *int64
}

// resolveExactDuplicateSkip decides what to do when an item's content hash
// already exists in the dedup cache. A hash collision is grounds for discard
// ONLY when a displayable prior broadcast from the SAME author exists (prior !=
// nil). A cross-author collision, a prior that has not finished processing, or a
// lookup error (all of which surface here as prior == nil) must NOT drop the
// item — it continues through normal processing so genuine content is never
// silently lost to an unverifiable "duplicate" verdict.
func resolveExactDuplicateSkip(prior *itemDal.DuplicateBroadcastReference) exactDuplicateDecision {
	if prior == nil {
		return exactDuplicateDecision{Discard: false}
	}
	dupID := prior.ItemID
	return exactDuplicateDecision{Discard: true, DuplicateOf: &dupID}
}

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
