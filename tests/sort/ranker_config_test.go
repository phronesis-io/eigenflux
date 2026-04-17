package sort_test

import (
	"os"
	"testing"

	"eigenflux_server/pkg/config"
	"github.com/stretchr/testify/assert"
)

func TestScoringConfigDefaults(t *testing.T) {
	for _, key := range []string{
		"SCORE_WEIGHT_SEMANTIC", "SCORE_WEIGHT_KEYWORD", "SCORE_WEIGHT_FRESHNESS",
		"SCORE_WEIGHT_DIVERSITY", "URGENCY_BOOST", "URGENCY_WINDOW",
		"MMR_LAMBDA", "EXPLORATION_SLOTS", "MIN_RELEVANCE_SCORE",
		"FRESHNESS_ALERT_OFFSET", "FRESHNESS_ALERT_SCALE", "FRESHNESS_ALERT_DECAY",
		"FRESHNESS_SUPPLY_OFFSET", "FRESHNESS_SUPPLY_SCALE", "FRESHNESS_SUPPLY_DECAY",
	} {
		os.Unsetenv(key)
	}

	cfg := config.Load()

	assert.InDelta(t, 0.4, cfg.ScoreWeightSemantic, 0.001)
	assert.InDelta(t, 0.2, cfg.ScoreWeightKeyword, 0.001)
	assert.InDelta(t, 0.3, cfg.ScoreWeightFreshness, 0.001)
	assert.InDelta(t, 0.1, cfg.ScoreWeightDiversity, 0.001)
	assert.InDelta(t, 0.5, cfg.UrgencyBoost, 0.001)
	assert.Equal(t, "24h", cfg.UrgencyWindow)
	assert.InDelta(t, 0.7, cfg.MMRLambda, 0.001)
	assert.Equal(t, 0, cfg.ExplorationSlots)
	assert.InDelta(t, 0.1, cfg.MinRelevanceScore, 0.001)

	assert.Equal(t, "2h", cfg.FreshnessAlertOffset)
	assert.Equal(t, "12h", cfg.FreshnessAlertScale)
	assert.InDelta(t, 0.5, cfg.FreshnessAlertDecay, 0.001)
	assert.Equal(t, "48h", cfg.FreshnessSupplyOffset)
	assert.Equal(t, "30d", cfg.FreshnessSupplyScale)
	assert.InDelta(t, 0.9, cfg.FreshnessSupplyDecay, 0.001)
}

func TestScoringConfigOverrides(t *testing.T) {
	os.Setenv("SCORE_WEIGHT_SEMANTIC", "0.5")
	os.Setenv("EXPLORATION_SLOTS", "0")
	os.Setenv("FRESHNESS_ALERT_OFFSET", "1h")
	os.Setenv("MIN_RELEVANCE_SCORE", "0.25")
	defer func() {
		os.Unsetenv("SCORE_WEIGHT_SEMANTIC")
		os.Unsetenv("EXPLORATION_SLOTS")
		os.Unsetenv("FRESHNESS_ALERT_OFFSET")
		os.Unsetenv("MIN_RELEVANCE_SCORE")
	}()

	cfg := config.Load()

	assert.InDelta(t, 0.5, cfg.ScoreWeightSemantic, 0.001)
	assert.Equal(t, 0, cfg.ExplorationSlots)
	assert.Equal(t, "1h", cfg.FreshnessAlertOffset)
	assert.InDelta(t, 0.25, cfg.MinRelevanceScore, 0.001)
}
