package rerank

import "eigenflux_server/rpc/sort/rank"

// Reranker runs an ordered list of policies and truncates to the requested
// limit. Construct with New; the zero value is unusable.
type Reranker struct {
	policies []Policy
}

// New builds a Reranker with the given policies. Policies run in the order
// they are passed; see the package doc comment for the canonical order.
func New(policies ...Policy) *Reranker {
	return &Reranker{policies: policies}
}

// Rerank runs the configured policies in order over cands, then truncates
// to limit (no truncation when limit <= 0). nil cands is returned unchanged.
func (r *Reranker) Rerank(cands []rank.Candidate, limit int) []rank.Candidate {
	if len(cands) == 0 {
		return cands
	}
	out := cands
	for _, p := range r.policies {
		out = p.Apply(out)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Policies returns the configured policy chain in order. Primarily exposed
// for tests and structured logging — do not mutate the returned slice.
func (r *Reranker) Policies() []Policy { return r.policies }
