package rank

import "fmt"

// BasicCandidate is the default Candidate adapter. Callers wrap their typed
// ranker outputs in BasicCandidate at the rerank boundary; the typed payload
// stays reachable through Source.
//
// Mutators (SetScore, AddReason) live on the concrete type rather than the
// interface so policies must type-assert to mutate — this keeps the
// Candidate view itself read-only.
type BasicCandidate struct {
	id       int64
	cType    CandidateType
	score    float64
	features map[string]float64
	source   any
	reasons  []string
}

// NewCandidate constructs a BasicCandidate. features may be nil; it is
// stored as-is and treated as read-only by the rerank layer.
func NewCandidate(id int64, cType CandidateType, score float64, features map[string]float64, source any) *BasicCandidate {
	return &BasicCandidate{
		id:       id,
		cType:    cType,
		score:    score,
		features: features,
		source:   source,
	}
}

func (b *BasicCandidate) ID() int64                    { return b.id }
func (b *BasicCandidate) Type() CandidateType          { return b.cType }
func (b *BasicCandidate) Score() float64               { return b.score }
func (b *BasicCandidate) Features() map[string]float64 { return b.features }
func (b *BasicCandidate) Source() any                  { return b.source }

// Fingerprint returns "<type>:<id>", the default dedup key. Two
// BasicCandidate values share a fingerprint iff they share both Type and ID.
func (b *BasicCandidate) Fingerprint() string {
	return fmt.Sprintf("%s:%d", b.cType, b.id)
}

// SetScore rewrites the rerank-time score.
func (b *BasicCandidate) SetScore(score float64) { b.score = score }

// AddReason appends a short tag describing why a policy touched this
// candidate (e.g., "slot:3", "normalize:minmax"). Useful for debug logs
// and offline analysis; not consumed by the reranker itself.
func (b *BasicCandidate) AddReason(tag string) {
	b.reasons = append(b.reasons, tag)
}

// Reasons returns the accumulated reason tags. The returned slice aliases
// the internal storage; callers must not mutate it.
func (b *BasicCandidate) Reasons() []string { return b.reasons }
