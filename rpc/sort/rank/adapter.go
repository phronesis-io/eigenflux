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
	id             int64
	cType          CandidateType
	score          float64
	features       map[string]float64
	source         any
	reasons        []string
	matchedIntents []string
	perIntentScore map[string]float64
	winningIntent  string
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

// SetScore rewrites the rerank-time score. Used by NormalizePolicy and any
// future policy that re-grades candidates.
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

// MatchedIntents lists which sub-intents recalled this candidate during
// SearchServices. Read by CoveragePolicy and the response builder.
func (b *BasicCandidate) MatchedIntents() []string { return b.matchedIntents }

// SetMatchedIntents replaces the matched-intents list.
func (b *BasicCandidate) SetMatchedIntents(s []string) { b.matchedIntents = s }

// PerIntentScore is intent name -> the typed serviceranker score from that
// sub-intent's recall lane. Used by the replay log and response builder;
// not consumed by rerank policies for ordering.
func (b *BasicCandidate) PerIntentScore() map[string]float64 { return b.perIntentScore }

// SetPerIntentScore replaces the per-intent score map.
func (b *BasicCandidate) SetPerIntentScore(m map[string]float64) { b.perIntentScore = m }

// WinningIntent is argmax(perIntentScore[name] * importance[name]) — the
// single sub-intent that drove the aggregate score for this candidate.
func (b *BasicCandidate) WinningIntent() string { return b.winningIntent }

// SetWinningIntent records the winning intent.
func (b *BasicCandidate) SetWinningIntent(s string) { b.winningIntent = s }
