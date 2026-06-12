// Package rerank composes ordered policies that transform a Candidate slice
// into the final display order.
//
// The reranker is intentionally dumb: it runs the policies in the order the
// caller supplied, then truncates to the configured limit. Each policy is a
// pure transform — given a slice it returns one. Cross-policy state is not
// supported; if a policy needs to know which positions were already touched
// it should infer this from candidate reasons or its own indexing.
//
// The canonical order for a mixed item+service surface is:
//
//	DedupPolicy → NormalizePolicy → BoundsPolicy → RatioPolicy → SlotPolicy
//
// (dedup first so later policies see no duplicates; normalize before any
// type-aware reshuffling; bounds to cap each type; ratio to interleave;
// slot last so positional overrides win.)
package rerank

import "eigenflux_server/rpc/sort/rank"

// Policy transforms a candidate slice. Implementations must be pure (no I/O,
// no goroutines) and tolerate empty input.
type Policy interface {
	// Name is a short stable identifier used in logs and reason tags.
	Name() string

	// Apply returns the transformed slice. Implementations may return the
	// input slice modified in place, a re-sliced view, or a brand-new slice.
	// Callers must use the return value and not the input afterwards.
	Apply(cands []rank.Candidate) []rank.Candidate
}
