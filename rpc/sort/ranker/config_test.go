package ranker

import (
	"testing"
	"time"

	"eigenflux_server/pkg/config"

	"github.com/stretchr/testify/assert"
)

func TestNewRankerConfigFromConfig(t *testing.T) {
	cfg := &config.Config{
		ScoreWeightSemantic:   0.4,
		ScoreWeightKeyword:    0.2,
		ScoreWeightFreshness:  0.3,
		ScoreWeightDiversity:  0.1,
		UrgencyBoost:          0.5,
		UrgencyWindow:         "24h",
		MMRLambda:             0.7,
		ExplorationSlots:      1,
		MinRelevanceScore:     0.0,
		FreshnessOffset:       "12h",
		FreshnessScale:        "7d",
		FreshnessDecay:        0.8,
		FreshnessAlertOffset:  "2h",
		FreshnessAlertScale:   "12h",
		FreshnessAlertDecay:   0.5,
		FreshnessSupplyOffset: "48h",
		FreshnessSupplyScale:  "30d",
		FreshnessSupplyDecay:  0.9,
	}

	rc := NewRankerConfig(cfg)
	assert.InDelta(t, 0.4, rc.Alpha, 0.001)
	assert.InDelta(t, 0.2, rc.Beta, 0.001)
	assert.InDelta(t, 0.3, rc.Gamma, 0.001)
	assert.InDelta(t, 0.1, rc.Delta, 0.001)
	assert.InDelta(t, 0.5, rc.UrgencyBoost, 0.001)
	assert.Equal(t, 24*time.Hour, rc.UrgencyWindow)
	assert.InDelta(t, 0.7, rc.MMRLambda, 0.001)
	assert.Equal(t, 1, rc.ExplorationSlots)
	assert.InDelta(t, 0.0, rc.MinRelevanceScore, 0.001)

	assert.Equal(t, 2*time.Hour, rc.Freshness["alert"].Offset)
	assert.Equal(t, 12*time.Hour, rc.Freshness["alert"].Scale)
	assert.InDelta(t, 0.5, rc.Freshness["alert"].Decay, 0.001)

	assert.Equal(t, 48*time.Hour, rc.Freshness["supply"].Offset)
	assert.Equal(t, 30*24*time.Hour, rc.Freshness["supply"].Scale)
	assert.InDelta(t, 0.9, rc.Freshness["supply"].Decay, 0.001)

	// info and demand use the base freshness values
	assert.Equal(t, 12*time.Hour, rc.Freshness["info"].Offset)
	assert.Equal(t, 7*24*time.Hour, rc.Freshness["info"].Scale)
	assert.InDelta(t, 0.8, rc.Freshness["info"].Decay, 0.001)
}

func TestParseDuration(t *testing.T) {
	assert.Equal(t, 2*time.Hour, parseDuration("2h", 0))
	assert.Equal(t, 30*24*time.Hour, parseDuration("30d", 0))
	assert.Equal(t, 7*24*time.Hour, parseDuration("7d", 0))
	assert.Equal(t, 12*time.Hour, parseDuration("12h", 0))
	assert.Equal(t, 5*time.Minute, parseDuration("5m", 0))
	assert.Equal(t, time.Hour, parseDuration("", time.Hour))        // fallback
	assert.Equal(t, time.Hour, parseDuration("invalid", time.Hour)) // fallback
}
