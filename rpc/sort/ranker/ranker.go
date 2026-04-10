package ranker

import (
	"time"

	"eigenflux_server/pkg/embedding"
	sortDal "eigenflux_server/rpc/sort/dal"
)

// UserProfile holds profile data needed for scoring.
type UserProfile struct {
	Keywords  []string
	Domains   []string
	Geo       string
	Embedding []float32
}

// ScoreBreakdown holds per-signal scores for replay log analysis.
type ScoreBreakdown struct {
	Semantic  float64 `json:"semantic"`
	Keyword   float64 `json:"keyword"`
	Freshness float64 `json:"freshness"`
	Total     float64 `json:"total"`
	IsDraft   bool    `json:"is_draft"`
}

// RankedItem is a scored item returned by the Ranker.
type RankedItem struct {
	ItemID int64
	Score  float64
	Scores ScoreBreakdown
}

// Ranker scores and re-ranks ES candidates.
type Ranker struct {
	config *RankerConfig
}

func New(cfg *RankerConfig) *Ranker {
	return &Ranker{config: cfg}
}

// Rank scores candidates and returns top-limit items sorted by relevance score.
// MMR diversity selection is implemented (see rankMMR) but disabled for now.
func (r *Ranker) Rank(candidates []sortDal.Item, profile *UserProfile, limit int) []RankedItem {
	if len(candidates) == 0 {
		return nil
	}

	now := time.Now()

	// Compute relevance scores
	type scored struct {
		idx    int
		score  float64
		scores ScoreBreakdown
	}
	items := make([]scored, len(candidates))
	for i, item := range candidates {
		bd := r.scoreItem(item, profile, now)
		items[i] = scored{idx: i, score: bd.Total, scores: bd}
	}

	// Sort by score descending (selection sort, N is small after ES recall)
	for i := 0; i < len(items) && i < limit; i++ {
		best := i
		for j := i + 1; j < len(items); j++ {
			if items[j].score > items[best].score {
				best = j
			}
		}
		items[i], items[best] = items[best], items[i]
	}

	if len(items) > limit {
		items = items[:limit]
	}

	selected := make([]RankedItem, len(items))
	for i, s := range items {
		selected[i] = RankedItem{
			ItemID: candidates[s.idx].ID,
			Score:  s.score,
			Scores: s.scores,
		}
	}
	return selected
}

// rankMMR selects top-limit items using Maximal Marginal Relevance for diversity.
// Currently unused — kept for future activation.
func (r *Ranker) rankMMR(candidates []sortDal.Item, profile *UserProfile, limit int) []RankedItem {
	if len(candidates) == 0 {
		return nil
	}

	now := time.Now()

	breakdowns := make([]ScoreBreakdown, len(candidates))
	for i, item := range candidates {
		breakdowns[i] = r.scoreItem(item, profile, now)
	}

	selected := make([]RankedItem, 0, limit)
	used := make([]bool, len(candidates))

	for len(selected) < limit {
		bestIdx := -1
		bestMMR := -1e18

		for i := range candidates {
			if used[i] {
				continue
			}

			maxSim := r.maxSimToSelected(candidates, i, selected)
			mmr := r.config.MMRLambda*breakdowns[i].Total - (1-r.config.MMRLambda)*maxSim

			if mmr > bestMMR {
				bestMMR = mmr
				bestIdx = i
			}
		}

		if bestIdx < 0 {
			break
		}

		used[bestIdx] = true
		selected = append(selected, RankedItem{
			ItemID: candidates[bestIdx].ID,
			Score:  breakdowns[bestIdx].Total,
			Scores: breakdowns[bestIdx],
		})
	}

	return selected
}

// maxSimToSelected computes max cosine similarity between candidate and all selected items.
func (r *Ranker) maxSimToSelected(candidates []sortDal.Item, candidateIdx int, selected []RankedItem) float64 {
	if len(selected) == 0 {
		return 0.0
	}
	maxSim := 0.0
	candidateEmb := candidates[candidateIdx].Embedding
	for _, sel := range selected {
		for j := range candidates {
			if candidates[j].ID == sel.ItemID {
				sim := embedding.CosineSimilarity(candidateEmb, candidates[j].Embedding)
				if sim > maxSim {
					maxSim = sim
				}
				break
			}
		}
	}
	return maxSim
}

// scoreItem computes relevance score with dynamic weight normalization and
// multiplicative freshness.
//
// Formula:
//
//	relevance = Σ(w_i * signal_i) / Σ(w_i)   (only active signals)
//	total     = relevance * freshnessMultiplier
//
// Active signals: semantic (α) when both profile and item have embeddings,
// keyword (β) always. δ (diversity) is handled by MMR selection, not here.
// Inactive weights are automatically redistributed by the normalization.
//
// Freshness multiplier: (1 − γ) + γ × rawFreshness.  γ controls strength:
// γ=0 → freshness has no effect; γ=1 → old items can decay to 0.
func (r *Ranker) scoreItem(item sortDal.Item, profile *UserProfile, now time.Time) ScoreBreakdown {
	isDraft := len(item.Keywords) == 0 && item.Type == ""

	semSim := semanticSimilarity(profile.Embedding, item.Embedding)
	kwOvlp := keywordOverlap(profile.Keywords, profile.Domains, item.Keywords, item.Domains)
	fresh := freshnessScore(r.config, item.Type, item.UpdatedAt, item.ExpireTime, now)

	// Weighted relevance with dynamic normalization
	var weightSum, relevance float64
	hasSemantic := len(profile.Embedding) > 0 && len(item.Embedding) > 0
	if hasSemantic {
		weightSum += r.config.Alpha
		relevance += r.config.Alpha * semSim
	}
	weightSum += r.config.Beta
	relevance += r.config.Beta * kwOvlp
	if weightSum > 0 {
		relevance /= weightSum
	}

	// Freshness as multiplier: floor at (1−γ), ceiling at 1.0
	freshMul := (1 - r.config.Gamma) + r.config.Gamma*fresh
	total := relevance * freshMul

	if isDraft {
		total *= r.config.DraftDampening
	}

	return ScoreBreakdown{
		Semantic:  semSim,
		Keyword:   kwOvlp,
		Freshness: fresh,
		Total:     total,
		IsDraft:   isDraft,
	}
}
