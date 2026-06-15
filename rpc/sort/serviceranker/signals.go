package serviceranker

import "eigenflux_server/pkg/embedding"

// semanticScore computes cosine similarity between query and service embeddings.
func semanticScore(queryEmbedding, serviceEmbedding []float32) float64 {
	return embedding.CosineSimilarity(queryEmbedding, serviceEmbedding)
}

// keywordScore normalizes an ES BM25 _score to [0,1].
func keywordScore(esScore, maxScore float64) float64 {
	if maxScore <= 0 {
		return 0.0
	}
	v := esScore / maxScore
	if v > 1.0 {
		return 1.0
	}
	return v
}

// successRateScore is a direct passthrough of the success rate in [0,1].
func successRateScore(rate float64) float64 {
	return rate
}

// latencyScore returns 1 - min(avgMs/maxMs, 1.0); lower latency is better.
func latencyScore(avgMs, maxMs int64) float64 {
	if maxMs <= 0 {
		return 0.0
	}
	v := float64(avgMs) / float64(maxMs)
	if v > 1.0 {
		v = 1.0
	}
	return 1.0 - v
}

// priceScore returns 1 - min(amount/maxAmount, 1.0); lower price is better.
func priceScore(amount, maxAmount int64) float64 {
	if maxAmount <= 0 {
		return 0.0
	}
	v := float64(amount) / float64(maxAmount)
	if v > 1.0 {
		v = 1.0
	}
	return 1.0 - v
}

// deadlineScore returns 1 - min(deadline/maxDeadline, 1.0); shorter deadline is better.
func deadlineScore(deadline, maxDeadline int64) float64 {
	if maxDeadline <= 0 {
		return 0.0
	}
	v := float64(deadline) / float64(maxDeadline)
	if v > 1.0 {
		v = 1.0
	}
	return 1.0 - v
}
