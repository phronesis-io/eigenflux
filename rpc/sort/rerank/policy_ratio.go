package rerank

import (
	"sort"

	"eigenflux_server/rpc/sort/rank"
)

// RatioPolicy interleaves candidates so that every CycleSize positions
// emit TypeCounts[t] of each type. For example, CycleSize=6 with
// {item:5, service:1} emits 5 items then 1 service repeatedly.
//
// Within each type the highest-scoring candidates fill slots first.
//
// If one type runs out before the cycle is satisfied, the position falls
// through to any non-empty queue (preferring the type with the largest
// remaining queue, ties broken by CandidateType string order for
// determinism). This makes the policy degrade gracefully when recall is
// thin.
//
// Types not listed in TypeCounts are appended after the interleaved tail,
// sorted by score desc, so they don't silently disappear.
type RatioPolicy struct {
	CycleSize  int
	TypeCounts map[rank.CandidateType]int
}

func (p *RatioPolicy) Name() string { return "ratio" }

func (p *RatioPolicy) Apply(cands []rank.Candidate) []rank.Candidate {
	if len(cands) == 0 || p.CycleSize <= 0 || len(p.TypeCounts) == 0 {
		return cands
	}

	queues := map[rank.CandidateType][]rank.Candidate{}
	var leftover []rank.Candidate
	for _, c := range cands {
		if _, want := p.TypeCounts[c.Type()]; want {
			queues[c.Type()] = append(queues[c.Type()], c)
		} else {
			leftover = append(leftover, c)
		}
	}
	for _, q := range queues {
		sort.SliceStable(q, func(i, j int) bool { return q[i].Score() > q[j].Score() })
	}

	out := make([]rank.Candidate, 0, len(cands))
	cycle := buildCycle(p.TypeCounts, p.CycleSize)

	pos := 0
	for {
		want := cycle[pos%len(cycle)]
		if c, ok := popFront(queues, want); ok {
			tagCandidate(c, "ratio")
			out = append(out, c)
			pos++
			continue
		}
		// Underflow on the desired type — fall through to any non-empty queue.
		if c, ok := popFromLargest(queues); ok {
			tagCandidate(c, "ratio:fallback")
			out = append(out, c)
			pos++
			continue
		}
		break
	}

	if len(leftover) > 0 {
		sort.SliceStable(leftover, func(i, j int) bool { return leftover[i].Score() > leftover[j].Score() })
		out = append(out, leftover...)
	}
	return out
}

// buildCycle expands TypeCounts into a flat sequence of length CycleSize.
// Types are emitted in descending count order so the most-represented type
// gets evenly spread positions; ties broken by CandidateType string order.
// Excess length is truncated and underflow is left at the tail of the cycle
// taking the most-represented type to pad — that's an acceptable
// approximation for first iteration.
func buildCycle(counts map[rank.CandidateType]int, size int) []rank.CandidateType {
	type tc struct {
		t     rank.CandidateType
		count int
	}
	pairs := make([]tc, 0, len(counts))
	total := 0
	for t, n := range counts {
		if n <= 0 {
			continue
		}
		pairs = append(pairs, tc{t, n})
		total += n
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].t < pairs[j].t
	})
	if total == 0 {
		return nil
	}
	out := make([]rank.CandidateType, 0, size)
	for _, p := range pairs {
		for i := 0; i < p.count && len(out) < size; i++ {
			out = append(out, p.t)
		}
	}
	for len(out) < size && len(pairs) > 0 {
		out = append(out, pairs[0].t)
	}
	return out
}

func popFront(queues map[rank.CandidateType][]rank.Candidate, t rank.CandidateType) (rank.Candidate, bool) {
	q := queues[t]
	if len(q) == 0 {
		return nil, false
	}
	c := q[0]
	queues[t] = q[1:]
	return c, true
}

func popFromLargest(queues map[rank.CandidateType][]rank.Candidate) (rank.Candidate, bool) {
	var bestType rank.CandidateType
	bestLen := 0
	for t, q := range queues {
		if len(q) > bestLen || (len(q) == bestLen && t < bestType) {
			if len(q) > 0 {
				bestLen = len(q)
				bestType = t
			}
		}
	}
	if bestLen == 0 {
		return nil, false
	}
	return popFront(queues, bestType)
}
