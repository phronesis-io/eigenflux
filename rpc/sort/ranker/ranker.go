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

// RankedItem is a scored item returned by the Ranker.
type RankedItem struct {
	ItemID int64
	Score  float64
}

// Ranker scores and re-ranks ES candidates.
type Ranker struct {
	config *RankerConfig
}

func New(cfg *RankerConfig) *Ranker {
	return &Ranker{config: cfg}
}

// Rank scores candidates and returns top-limit items using MMR diversity selection.
func (r *Ranker) Rank(candidates []sortDal.Item, profile *UserProfile, limit int) []RankedItem {
	if len(candidates) == 0 {
		return nil
	}

	now := time.Now()

	// Phase 1: compute raw relevance scores
	relevanceScores := make([]float64, len(candidates))
	for i, item := range candidates {
		relevanceScores[i] = r.scoreItem(item, profile, now)
	}

	// Phase 2: MMR iterative selection
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
			mmr := r.config.MMRLambda*relevanceScores[i] - (1-r.config.MMRLambda)*maxSim

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
			Score:  relevanceScores[bestIdx],
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

// scoreItem computes raw relevance score for a single item.
func (r *Ranker) scoreItem(item sortDal.Item, profile *UserProfile, now time.Time) float64 {
	isDraft := len(item.Keywords) == 0 && item.Type == ""

	semSim := semanticSimilarity(profile.Embedding, item.Embedding)
	kwOvlp := keywordOverlap(profile.Keywords, profile.Domains, item.Keywords, item.Domains)
	fresh := freshnessScore(r.config, item.Type, item.UpdatedAt, item.ExpireTime, now)

	score := r.config.Alpha*semSim + r.config.Beta*kwOvlp + r.config.Gamma*fresh

	if isDraft {
		score *= r.config.DraftDampening
	}

	return score
}
