package rerank

import (
	"fmt"
	"sort"

	"eigenflux_server/rpc/sort/rank"
)

// InjectPolicy force-inserts up to Count candidates matching Match into the
// reserved Positions, guaranteeing they survive a later top-N truncation even
// when their score would not otherwise place them there.
//
// It is deliberately channel-agnostic: Match is a predicate supplied by the
// caller (typically "this candidate came from recall channel X"), so the same
// policy backs any future forced-insertion need — only the predicate changes.
//
// Candidates are assumed to arrive in descending score order, so the first
// Count matches are the highest-scoring ones. This gives "relevance-first,
// coverage-fallback" for free: a matched (high-score) candidate is injected
// ahead of an unmatched (low-score) one, but when only low-score matches exist
// they are still injected.
//
// Positions are 0-indexed target slots in the final ordering. When empty the
// injected candidates fill the front (0, 1, ...). Displaced candidates shift
// down; relative order is otherwise preserved.
type InjectPolicy struct {
	Match     func(rank.Candidate) bool
	Count     int
	Positions []int
}

func (p *InjectPolicy) Name() string { return "inject" }

func (p *InjectPolicy) Apply(cands []rank.Candidate) []rank.Candidate {
	if p.Match == nil || p.Count <= 0 || len(cands) == 0 {
		return cands
	}

	// Pick the highest-scoring matches (input is score-ordered), capped at
	// both Count and the number of target positions.
	want := p.Count
	if len(p.Positions) > 0 && len(p.Positions) < want {
		want = len(p.Positions)
	}

	toInject := make([]rank.Candidate, 0, want)
	injectIdx := make(map[int]struct{}, want)
	for i, c := range cands {
		if len(toInject) >= want {
			break
		}
		if p.Match(c) {
			toInject = append(toInject, c)
			injectIdx[i] = struct{}{}
		}
	}
	if len(toInject) == 0 {
		return cands
	}

	// Everything not being injected, in original order.
	rest := make([]rank.Candidate, 0, len(cands)-len(toInject))
	for i, c := range cands {
		if _, ok := injectIdx[i]; !ok {
			rest = append(rest, c)
		}
	}

	// Target positions, ascending, so sequential insertion lands each element
	// at its intended absolute index.
	targets := make([]int, len(toInject))
	if len(p.Positions) > 0 {
		copy(targets, p.Positions[:len(toInject)])
		sort.Ints(targets)
	} else {
		for i := range targets {
			targets[i] = i
		}
	}

	out := rest
	for i, c := range toInject {
		pos := max(targets[i], 0)
		pos = min(pos, len(out))
		out = append(out, nil)
		copy(out[pos+1:], out[pos:])
		out[pos] = c
		tagCandidate(c, fmt.Sprintf("inject:%d", pos))
	}
	return out
}
