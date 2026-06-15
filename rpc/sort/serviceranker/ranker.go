package serviceranker

import sortdal "eigenflux_server/rpc/sort/dal"

// RankedService is a scored service candidate returned by the ServiceRanker.
type RankedService struct {
	ServiceID int64
	Score     float64
	Breakdown ServiceScoreBreakdown
}

// ServiceScoreBreakdown holds per-signal scores for analysis.
type ServiceScoreBreakdown struct {
	Semantic float64 `json:"semantic"`
	Keyword  float64 `json:"keyword"`
	Success  float64 `json:"success"`
	Latency  float64 `json:"latency"`
	Price    float64 `json:"price"`
	Deadline float64 `json:"deadline"`
	Total    float64 `json:"total"`
}

// ServiceRanker scores and sorts service candidates using 6-signal weighted scoring.
type ServiceRanker struct {
	config *ServiceRankerConfig
}

// New creates a ServiceRanker with the given config.
func New(cfg *ServiceRankerConfig) *ServiceRanker {
	return &ServiceRanker{config: cfg}
}

// Rank scores candidates and returns top-limit services sorted by total score descending.
func (r *ServiceRanker) Rank(candidates []sortdal.ServiceDoc, queryEmbedding []float32, limit int) []RankedService {
	if len(candidates) == 0 {
		return nil
	}

	// Find max ES score for keyword normalization
	maxESScore := 0.0
	for _, c := range candidates {
		if c.Score > maxESScore {
			maxESScore = c.Score
		}
	}

	type scored struct {
		idx       int
		score     float64
		breakdown ServiceScoreBreakdown
	}
	items := make([]scored, len(candidates))
	for i, c := range candidates {
		bd := r.scoreService(c, queryEmbedding, maxESScore)
		items[i] = scored{idx: i, score: bd.Total, breakdown: bd}
	}

	// Selection sort by score descending (N is small after ES recall)
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

	result := make([]RankedService, len(items))
	for i, s := range items {
		result[i] = RankedService{
			ServiceID: candidates[s.idx].ServiceID,
			Score:     s.score,
			Breakdown: s.breakdown,
		}
	}
	return result
}

func (r *ServiceRanker) scoreService(c sortdal.ServiceDoc, queryEmbedding []float32, maxESScore float64) ServiceScoreBreakdown {
	sem := semanticScore(queryEmbedding, c.Embedding)
	kw := keywordScore(c.Score, maxESScore)
	suc := successRateScore(c.SuccessRate)
	lat := latencyScore(c.AvgLatencyMs, r.config.MaxLatencyMs)
	pri := priceScore(c.AmountAtomic, r.config.MaxPriceAtomic)
	dead := deadlineScore(c.DeliveryDeadlineMs, r.config.MaxDeadlineMs)

	total := r.config.SemanticWeight*sem +
		r.config.KeywordWeight*kw +
		r.config.SuccessWeight*suc +
		r.config.LatencyWeight*lat +
		r.config.PriceWeight*pri +
		r.config.DeadlineWeight*dead

	return ServiceScoreBreakdown{
		Semantic: sem,
		Keyword:  kw,
		Success:  suc,
		Latency:  lat,
		Price:    pri,
		Deadline: dead,
		Total:    total,
	}
}
