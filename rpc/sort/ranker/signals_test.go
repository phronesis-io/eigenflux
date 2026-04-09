package ranker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGaussianDecay(t *testing.T) {
	now := time.Now()

	// Within offset → score = 1.0
	recent := now.Add(-1 * time.Hour)
	score := gaussianDecay(recent, now, 2*time.Hour, 12*time.Hour, 0.5)
	assert.InDelta(t, 1.0, score, 0.01)

	// At offset → score ≈ 1.0
	atOffset := now.Add(-2 * time.Hour)
	score = gaussianDecay(atOffset, now, 2*time.Hour, 12*time.Hour, 0.5)
	assert.InDelta(t, 1.0, score, 0.05)

	// At offset + scale → score ≈ decay
	atScale := now.Add(-14 * time.Hour)
	score = gaussianDecay(atScale, now, 2*time.Hour, 12*time.Hour, 0.5)
	assert.InDelta(t, 0.5, score, 0.05)

	// Very old → approaches 0
	veryOld := now.Add(-30 * 24 * time.Hour)
	score = gaussianDecay(veryOld, now, 2*time.Hour, 12*time.Hour, 0.5)
	assert.Less(t, score, 0.1)
}

func TestFreshnessScore_PerType(t *testing.T) {
	cfg := &RankerConfig{
		Freshness: map[string]FreshnessParams{
			"alert":  {Offset: 2 * time.Hour, Scale: 12 * time.Hour, Decay: 0.5},
			"info":   {Offset: 12 * time.Hour, Scale: 7 * 24 * time.Hour, Decay: 0.8},
			"supply": {Offset: 48 * time.Hour, Scale: 30 * 24 * time.Hour, Decay: 0.9},
			"demand": {Offset: 12 * time.Hour, Scale: 7 * 24 * time.Hour, Decay: 0.8},
		},
		UrgencyBoost:  0.5,
		UrgencyWindow: 24 * time.Hour,
	}
	now := time.Now()

	// Alert at 6 hours should decay more than info at 6 hours
	sixHoursAgo := now.Add(-6 * time.Hour)
	alertScore := freshnessScore(cfg, "alert", sixHoursAgo, nil, now)
	infoScore := freshnessScore(cfg, "info", sixHoursAgo, nil, now)
	assert.Less(t, alertScore, infoScore, "alert should decay faster than info")

	// Supply at 24h should be higher than info at 24h
	oneDayAgo := now.Add(-24 * time.Hour)
	supplyScore := freshnessScore(cfg, "supply", oneDayAgo, nil, now)
	infoScore2 := freshnessScore(cfg, "info", oneDayAgo, nil, now)
	assert.Greater(t, supplyScore, infoScore2, "supply should decay slower than info")
}

func TestFreshnessScore_DemandUrgency(t *testing.T) {
	cfg := &RankerConfig{
		Freshness: map[string]FreshnessParams{
			"demand": {Offset: 12 * time.Hour, Scale: 7 * 24 * time.Hour, Decay: 0.8},
		},
		UrgencyBoost:  0.5,
		UrgencyWindow: 24 * time.Hour,
	}
	now := time.Now()
	updatedAt := now.Add(-1 * time.Hour)

	// Demand expiring in 2 hours → high urgency
	soonExpire := now.Add(2 * time.Hour)
	urgentScore := freshnessScore(cfg, "demand", updatedAt, &soonExpire, now)

	// Demand expiring in 7 days → low urgency
	laterExpire := now.Add(7 * 24 * time.Hour)
	normalScore := freshnessScore(cfg, "demand", updatedAt, &laterExpire, now)

	assert.Greater(t, urgentScore, normalScore, "soon-expiring demand should score higher")

	// Expired → 0
	expired := now.Add(-1 * time.Hour)
	expiredScore := freshnessScore(cfg, "demand", updatedAt, &expired, now)
	assert.InDelta(t, 0.0, expiredScore, 0.001)
}

func TestKeywordOverlap(t *testing.T) {
	score := keywordOverlap(
		[]string{"AI", "blockchain"}, []string{"tech", "AI"},
		[]string{"AI", "NLP", "blockchain"}, []string{"tech", "AI", "finance"},
	)
	assert.Greater(t, score, 0.5)
	assert.LessOrEqual(t, score, 1.0)
}

func TestKeywordOverlap_NoOverlap(t *testing.T) {
	score := keywordOverlap(
		[]string{"AI"}, []string{"tech"},
		[]string{"cooking"}, []string{"food"},
	)
	assert.InDelta(t, 0.0, score, 0.001)
}

func TestKeywordOverlap_Empty(t *testing.T) {
	score := keywordOverlap(nil, nil, []string{"AI"}, []string{"tech"})
	assert.InDelta(t, 0.0, score, 0.001)
}
