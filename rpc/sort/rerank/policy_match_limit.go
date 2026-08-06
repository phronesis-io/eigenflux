package rerank

import "eigenflux_server/rpc/sort/rank"

// MatchLimitPolicy keeps at most MaxCount candidates accepted by Match.
// Candidates are expected in display order, so the highest-ranked matches
// survive. Non-matching candidates pass through unchanged.
type MatchLimitPolicy struct {
	Match     func(rank.Candidate) bool
	MaxCount  int
	ReasonTag string
}

func (p *MatchLimitPolicy) Name() string { return "match_limit" }

func (p *MatchLimitPolicy) Apply(cands []rank.Candidate) []rank.Candidate {
	if len(cands) == 0 || p.Match == nil || p.MaxCount < 0 {
		return cands
	}

	matched := 0
	out := make([]rank.Candidate, 0, len(cands))
	for _, c := range cands {
		if !p.Match(c) {
			out = append(out, c)
			continue
		}
		if matched >= p.MaxCount {
			tagCandidate(c, "match_limit:"+p.ReasonTag)
			continue
		}
		matched++
		out = append(out, c)
	}
	return out
}
