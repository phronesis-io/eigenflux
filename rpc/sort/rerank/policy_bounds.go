package rerank

import (
	"fmt"
	"sort"

	"eigenflux_server/rpc/sort/rank"
)

// Bound describes the per-type allowance enforced by BoundsPolicy.
//
// Ceiling > 0 trims the type to the top-N by score. Ceiling <= 0 means no
// upper bound for that type.
//
// Floor > 0 demands that at least Floor candidates of that type appear in
// the top-Limit positions (Limit comes from BoundsPolicy.Limit). When the
// natural score order would place fewer than Floor candidates of the type
// inside the window, BoundsPolicy swaps the lowest-scoring positions of
// other types inside the window for the highest-scoring matching
// candidates from outside the window — a "tail-replace" promotion. If
// BoundsPolicy.Limit is unset or no matching candidates exist outside the
// window, Floor degrades to informational (cannot fabricate candidates the
// upstream ranker did not produce).
type Bound struct {
	Floor   int
	Ceiling int
}

// BoundsPolicy enforces per-type ceilings and (when Limit > 0) floors via
// tail replacement. Candidate types not listed in Bounds pass through
// unchanged.
type BoundsPolicy struct {
	Bounds map[rank.CandidateType]Bound
	// Limit is the window size used for floor enforcement. Typically the
	// caller-requested result limit. Floors are evaluated against the first
	// Limit positions after the ceiling pass. Limit <= 0 disables floor
	// enforcement (Floor remains informational).
	Limit int
}

func (p *BoundsPolicy) Name() string { return "bounds" }

func (p *BoundsPolicy) Apply(cands []rank.Candidate) []rank.Candidate {
	if len(cands) == 0 || len(p.Bounds) == 0 {
		return cands
	}

	// Stable sort by score desc so equal-scored candidates keep their
	// arrival order — useful when DedupPolicy ran first.
	sorted := make([]rank.Candidate, len(cands))
	copy(sorted, cands)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Score() > sorted[j].Score()
	})

	counts := make(map[rank.CandidateType]int, len(p.Bounds))
	out := sorted[:0]
	for _, c := range sorted {
		b, bounded := p.Bounds[c.Type()]
		if !bounded {
			out = append(out, c)
			continue
		}
		if b.Ceiling > 0 && counts[c.Type()] >= b.Ceiling {
			tagCandidate(c, "bounds:ceiling")
			continue
		}
		counts[c.Type()]++
		out = append(out, c)
	}

	if p.Limit > 0 {
		out = p.enforceFloors(out)
	}
	return out
}

// enforceFloors tail-replaces lowest-scoring non-matching positions inside
// the top-Limit window with the highest-scoring matching candidates from
// outside the window. Iterates types in deterministic order so the result
// is stable when multiple types have unmet floors.
func (p *BoundsPolicy) enforceFloors(out []rank.Candidate) []rank.Candidate {
	cutoff := p.Limit
	if cutoff > len(out) {
		cutoff = len(out)
	}
	if cutoff == 0 {
		return out
	}

	types := make([]rank.CandidateType, 0, len(p.Bounds))
	for t, b := range p.Bounds {
		if b.Floor > 0 {
			types = append(types, t)
		}
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })

	for _, t := range types {
		floor := p.Bounds[t].Floor

		inWindow := 0
		for i := 0; i < cutoff; i++ {
			if out[i].Type() == t {
				inWindow++
			}
		}
		need := floor - inWindow
		if need <= 0 {
			continue
		}

		// Promote the next `need` highest-scoring matches that sit beyond
		// the window. They appear in score-desc order already because the
		// caller sorted by score before calling enforceFloors.
		for k := 0; k < need; k++ {
			srcIdx := -1
			for i := cutoff; i < len(out); i++ {
				if out[i].Type() == t {
					srcIdx = i
					break
				}
			}
			if srcIdx < 0 {
				break // Nothing more to promote.
			}

			// Displace the lowest-scoring non-matching slot inside the window.
			dstIdx := -1
			for j := cutoff - 1; j >= 0; j-- {
				if out[j].Type() != t {
					dstIdx = j
					break
				}
			}
			if dstIdx < 0 {
				break // Entire window is already type t — nothing to displace.
			}

			out[dstIdx], out[srcIdx] = out[srcIdx], out[dstIdx]
			tagCandidate(out[dstIdx], fmt.Sprintf("bounds:floor:%s", t))
			tagCandidate(out[srcIdx], "bounds:displaced")
		}
	}
	return out
}

// tagCandidate appends a reason tag if the candidate is a *BasicCandidate;
// silently no-ops on other implementations.
func tagCandidate(c rank.Candidate, tag string) {
	if bc, ok := c.(*rank.BasicCandidate); ok {
		bc.AddReason(tag)
	}
}
