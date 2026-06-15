package rerank

import (
	"fmt"
	"sort"

	"eigenflux_server/rpc/sort/rank"
)

// SlotRule pins a 0-indexed position to a specific candidate type. If no
// candidate of that type is available the slot keeps its current occupant.
type SlotRule struct {
	Position int
	Type     rank.CandidateType
}

// SlotPolicy enforces fixed-position type constraints. For each rule, the
// top-scoring unused candidate of the required type is swapped into the
// target position; the candidate previously at that position is moved to
// where the promoted candidate was.
//
// Place this policy last in the chain: positional overrides should win
// over interleave rhythm.
type SlotPolicy struct {
	Slots []SlotRule
}

func (p *SlotPolicy) Name() string { return "slot" }

func (p *SlotPolicy) Apply(cands []rank.Candidate) []rank.Candidate {
	if len(cands) == 0 || len(p.Slots) == 0 {
		return cands
	}

	out := make([]rank.Candidate, len(cands))
	copy(out, cands)

	// Process slot rules in ascending position order so an earlier slot
	// never displaces a later one we already placed.
	rules := make([]SlotRule, len(p.Slots))
	copy(rules, p.Slots)
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Position < rules[j].Position })

	placed := make(map[int]struct{}, len(rules))

	for _, rule := range rules {
		if rule.Position < 0 || rule.Position >= len(out) {
			continue
		}
		if out[rule.Position].Type() == rule.Type {
			tagCandidate(out[rule.Position], fmt.Sprintf("slot:%d:already", rule.Position))
			placed[rule.Position] = struct{}{}
			continue
		}

		// Find the highest-scoring candidate of the desired type that we
		// haven't already placed.
		bestIdx := -1
		var bestScore float64
		for i, c := range out {
			if i == rule.Position {
				continue
			}
			if _, locked := placed[i]; locked {
				continue
			}
			if c.Type() != rule.Type {
				continue
			}
			if bestIdx < 0 || c.Score() > bestScore {
				bestIdx = i
				bestScore = c.Score()
			}
		}
		if bestIdx < 0 {
			tagCandidate(out[rule.Position], fmt.Sprintf("slot:%d:miss", rule.Position))
			continue
		}
		out[rule.Position], out[bestIdx] = out[bestIdx], out[rule.Position]
		tagCandidate(out[rule.Position], fmt.Sprintf("slot:%d", rule.Position))
		placed[rule.Position] = struct{}{}
	}
	return out
}
