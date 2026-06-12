// Package rank defines the cross-type Candidate interface consumed by the
// rerank layer. Domain rankers (rpc/sort/ranker for items, rpc/sort/serviceranker
// for services) continue to emit their own typed results; callers wrap those at
// the rerank boundary via BasicCandidate and recover the typed payload through
// Candidate.Source.
//
// This package is intentionally tiny and dependency-free. The policy logic
// lives in rpc/sort/rerank.
package rank

// CandidateType identifies which domain a candidate came from. Add a new
// constant when a new typed ranker is wired into the rerank layer.
type CandidateType string

const (
	CandidateItem    CandidateType = "item"
	CandidateService CandidateType = "service"
)

// Candidate is the cross-type rerank input. It is read-only: rerank policies
// that need to change scores or attach reasons type-assert to *BasicCandidate
// (or whatever concrete type the caller wrapped). Implementations must be
// safe to call from a single goroutine; the reranker does not parallelise.
//
// Note: distinct from recallsource.Candidate, which represents one item
// fetched from a single recall channel and lives a layer below this one.
// The two coexist deliberately: recallsource.Candidate is the recall-time
// view, rank.Candidate is the rerank-time view.
type Candidate interface {
	// ID is the domain primary key. Two candidates with the same Type and ID
	// represent the same underlying entity.
	ID() int64

	// Type identifies which domain the candidate came from.
	Type() CandidateType

	// Score is the rerank-time score. Policies may rewrite it via the
	// concrete adapter (e.g., NormalizePolicy on *BasicCandidate).
	Score() float64

	// Features exposes signal values used by the upstream ranker
	// (e.g., "freshness", "latency"). The returned map is read-only; policies
	// must not mutate it.
	Features() map[string]float64

	// Source returns the original typed payload (e.g., *ranker.RankedItem).
	// Callers type-assert to recover it after reranking.
	Source() any

	// Fingerprint is the dedup key. Default "<type>:<id>".
	Fingerprint() string
}
