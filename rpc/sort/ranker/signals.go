package ranker

import (
	"math"
	"strings"
	"time"

	"eigenflux_server/pkg/embedding"
)

// semanticSimilarity computes cosine similarity between profile and item embeddings.
func semanticSimilarity(profileEmb, itemEmb []float32) float64 {
	return embedding.CosineSimilarity(profileEmb, itemEmb)
}

// profileSets holds pre-computed lowercase sets for a user profile,
// avoiding repeated map allocation per item.
type profileSets struct {
	keywords map[string]bool
	domains  map[string]bool
}

func buildProfileSets(profile *UserProfile) *profileSets {
	ps := &profileSets{
		keywords: make(map[string]bool, len(profile.Keywords)),
		domains:  make(map[string]bool, len(profile.Domains)),
	}
	for _, k := range profile.Keywords {
		ps.keywords[strings.ToLower(k)] = true
	}
	for _, d := range profile.Domains {
		ps.domains[strings.ToLower(d)] = true
	}
	return ps
}

// keywordOverlap computes normalized overlap between user and item keywords+domains.
func keywordOverlap(ps *profileSets, itemKeywords, itemDomains []string) float64 {
	kwOverlap := setOverlapPrecomputed(ps.keywords, itemKeywords)
	domOverlap := setOverlapPrecomputed(ps.domains, itemDomains)

	count := 0
	sum := 0.0
	if len(ps.keywords) > 0 && len(itemKeywords) > 0 {
		sum += kwOverlap
		count++
	}
	if len(ps.domains) > 0 && len(itemDomains) > 0 {
		sum += domOverlap
		count++
	}
	if count == 0 {
		return 0.0
	}
	return sum / float64(count)
}

// setOverlapPrecomputed returns |A ∩ B| / |B| using a pre-computed user set.
func setOverlapPrecomputed(userSet map[string]bool, item []string) float64 {
	if len(userSet) == 0 || len(item) == 0 {
		return 0.0
	}
	matched := 0
	for _, it := range item {
		if userSet[strings.ToLower(it)] {
			matched++
		}
	}
	return float64(matched) / float64(len(item))
}

// freshnessScore computes freshness based on broadcast_type.
func freshnessScore(cfg *RankerConfig, broadcastType string, updatedAt time.Time, expireTime *time.Time, now time.Time) float64 {
	if broadcastType == "demand" && expireTime != nil {
		return urgencyAwareFreshness(cfg, updatedAt, *expireTime, now)
	}
	params, ok := cfg.Freshness[broadcastType]
	if !ok {
		params = cfg.Freshness["info"]
	}
	return gaussianDecay(updatedAt, now, params.Offset, params.Scale, params.Decay)
}

func urgencyAwareFreshness(cfg *RankerConfig, updatedAt time.Time, expireTime time.Time, now time.Time) float64 {
	remaining := expireTime.Sub(now)
	if remaining <= 0 {
		return 0.0
	}
	params := cfg.Freshness["demand"]
	base := gaussianDecay(updatedAt, now, params.Offset, params.Scale, params.Decay)
	urgencyRatio := 1.0 - remaining.Seconds()/cfg.UrgencyWindow.Seconds()
	if urgencyRatio < 0 {
		urgencyRatio = 0
	}
	// Scale so that max output is 1.0: divide by (1 + UrgencyBoost) to normalize
	return base * (1.0 + cfg.UrgencyBoost*urgencyRatio) / (1.0 + cfg.UrgencyBoost)
}

// gaussianDecay implements ES-style Gaussian decay.
func gaussianDecay(updatedAt time.Time, now time.Time, offset, scale time.Duration, decay float64) float64 {
	age := now.Sub(updatedAt)
	if age <= offset {
		return 1.0
	}
	lnDecay := math.Log(decay)
	if lnDecay >= 0 {
		return 1.0
	}
	sigma := scale.Seconds() / math.Sqrt(-2*lnDecay)
	x := (age - offset).Seconds()
	return math.Exp(-0.5 * (x / sigma) * (x / sigma))
}
