package rerank

import "eigenflux_server/rpc/sort/rank"

// DedupPolicy keeps the first occurrence of each Fingerprint and drops the
// rest. Use it as the first policy in any chain so downstream policies
// never see two views of the same entity (e.g., when a service is recalled
// by both keyword and embedding channels and wrapped twice).
//
// Order matters: this policy preserves input order, so place it after the
// ranker has put higher-scoring duplicates first — usually that's the
// natural state on entry to the rerank stage.
type DedupPolicy struct{}

func (p *DedupPolicy) Name() string { return "dedup" }

func (p *DedupPolicy) Apply(cands []rank.Candidate) []rank.Candidate {
	if len(cands) == 0 {
		return cands
	}
	seen := make(map[string]struct{}, len(cands))
	out := cands[:0]
	for _, c := range cands {
		fp := c.Fingerprint()
		if _, dup := seen[fp]; dup {
			continue
		}
		seen[fp] = struct{}{}
		out = append(out, c)
	}
	return out
}
