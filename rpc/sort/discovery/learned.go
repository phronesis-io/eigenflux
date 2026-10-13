package discovery

import (
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/rpc/sort/discoverylr"
	"slices"
)

type LearnedRanker interface {
	ScoreBatch([][]float64) ([]discoverylr.Result, bool)
}

func learningVector(c Candidate) ([]float64, bool) {
	values := make([]float64, 0, len(discoverylr.FeatureNames))
	for _, key := range []string{"lexical", "semantic", "freshness", "quality"} {
		v, ok := c.Score.Features[key]
		if !ok || !finite(v) || v < 0 || v > 1 {
			return nil, false
		}
		values = append(values, v)
	}
	if !finite(c.Score.Relevance) || !finite(c.Score.Value) || c.Score.Relevance < 0 || c.Score.Relevance > 1 || c.Score.Value < 0 || c.Score.Value > 1 {
		return nil, false
	}
	values = append(values, c.Score.Relevance, c.Score.Value)
	flag := func(b bool) {
		v := 0.0
		if b {
			v = 1
		}
		values = append(values, v)
	}
	flag(slices.Contains(c.Score.Missing, "semantic"))
	flag(c.Context.Origin == "baseline")
	flag(c.Context.Origin == "friend")
	return values, true
}

func (e *Engine) rankLearned(candidates []Candidate, mode Mode) {
	if mode != Recommendation || e.Learned == nil {
		return
	}
	indices := []int{}
	vectors := [][]float64{}
	for i, c := range candidates {
		if c.Document.Ref.Type != Broadcast || !c.Score.Eligible {
			continue
		}
		v, ok := learningVector(c)
		if !ok {
			metrics.DiscoveryLRScoring.WithLabelValues("invalid_features").Inc()
			return
		}
		indices = append(indices, i)
		vectors = append(vectors, v)
	}
	if len(indices) == 0 {
		return
	}
	scores, ok := e.Learned.ScoreBatch(vectors)
	if !ok || len(scores) != len(indices) {
		metrics.DiscoveryLRScoring.WithLabelValues("fallback").Inc()
		return
	}
	metrics.DiscoveryLRScoring.WithLabelValues("scored").Inc()
	for i, index := range indices {
		result := scores[i]
		candidates[index].LR = &result
		candidates[index].FinalScore = result.Probability
	}
}

func (c Candidate) RankingScore() float64 {
	if c.LR != nil {
		return c.LR.Probability
	}
	return c.Score.Value
}
