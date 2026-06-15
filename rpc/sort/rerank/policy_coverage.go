// Package rerank — CoveragePolicy.
//
// CoveragePolicy guarantees that each protected sub-intent appears at least
// FloorPerIntent times in the top-Limit window after normalisation. Intended
// for SearchServices, where the input is a merged set of candidates from
// multiple per-sub-intent recalls.
//
// Unlike BoundsPolicy (which keys off rank.CandidateType), CoveragePolicy
// keys off string intent names attached to *rank.BasicCandidate.MatchedIntents.
// Intents whose importance is below ImportanceThreshold are not protected —
// they drift naturally on score.
//
// CoveragePolicy does NOT call AddReason: reasons would carry unbounded
// cardinality (one tag per intent name) and pollute replay-log aggregation.

package rerank

import (
	"sort"

	"eigenflux_server/rpc/sort/rank"
)

// CoveragePolicy implements rerank.Policy.
type CoveragePolicy struct {
	Limit               int                // top-N window
	FloorPerIntent      int                // each protected intent gets at least this many slots
	ImportanceThreshold float64            // intents with importance below this are exempt
	Importance          map[string]float64 // intent name -> importance in [0,1]; nil treats all as 1.0
}

// Name implements rerank.Policy.
func (p *CoveragePolicy) Name() string { return "coverage" }

// Apply implements rerank.Policy.
func (p *CoveragePolicy) Apply(cands []rank.Candidate) []rank.Candidate {
	if len(cands) == 0 || p.Limit <= 0 || p.FloorPerIntent <= 0 {
		return cands
	}

	out := make([]rank.Candidate, len(cands))
	copy(out, cands)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score() > out[j].Score() })

	windowSize := p.Limit
	if windowSize > len(out) {
		windowSize = len(out)
	}

	// locked tracks window slots that hold a candidate promoted to satisfy a
	// previously-processed protected intent. Locked slots are off-limits for
	// later eviction, preventing intent A's promotion from being undone by
	// intent B in the same pass.
	locked := make(map[int]bool, windowSize)
	for _, intent := range p.protectedIntents(out) {
		if countIntent(out[:windowSize], intent) >= p.FloorPerIntent {
			// Lock existing in-window slots that already match this intent so
			// later iterations cannot displace them either.
			for i := 0; i < windowSize; i++ {
				if hasIntent(out[i], intent) {
					locked[i] = true
				}
			}
			continue
		}
		outIdx := pickBestOutside(out, windowSize, intent)
		if outIdx < 0 {
			continue
		}
		inIdx := pickWorstInside(out, windowSize, intent, locked)
		if inIdx < 0 {
			continue
		}
		out[inIdx], out[outIdx] = out[outIdx], out[inIdx]
		locked[inIdx] = true
	}

	sort.SliceStable(out[:windowSize], func(i, j int) bool { return out[i].Score() > out[j].Score() })
	return out
}

// protectedIntents returns intent names whose importance meets the threshold,
// in alphabetical order for deterministic behaviour under tests.
func (p *CoveragePolicy) protectedIntents(cands []rank.Candidate) []string {
	seen := map[string]struct{}{}
	for _, c := range cands {
		bc, ok := c.(*rank.BasicCandidate)
		if !ok {
			continue
		}
		for _, name := range bc.MatchedIntents() {
			seen[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		if p.importanceOf(name) >= p.ImportanceThreshold {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (p *CoveragePolicy) importanceOf(name string) float64 {
	if p.Importance == nil {
		return 1.0
	}
	if v, ok := p.Importance[name]; ok {
		return v
	}
	return 0
}

func countIntent(cands []rank.Candidate, intent string) int {
	n := 0
	for _, c := range cands {
		if hasIntent(c, intent) {
			n++
		}
	}
	return n
}

func pickBestOutside(cands []rank.Candidate, window int, intent string) int {
	for i := window; i < len(cands); i++ {
		if hasIntent(cands[i], intent) {
			return i
		}
	}
	return -1
}

func pickWorstInside(cands []rank.Candidate, window int, intent string, locked map[int]bool) int {
	for i := window - 1; i >= 0; i-- {
		if locked[i] {
			continue
		}
		if !hasIntent(cands[i], intent) {
			return i
		}
	}
	return -1
}

func hasIntent(c rank.Candidate, intent string) bool {
	bc, ok := c.(*rank.BasicCandidate)
	if !ok {
		return false
	}
	for _, name := range bc.MatchedIntents() {
		if name == intent {
			return true
		}
	}
	return false
}
