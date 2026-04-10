package ranker

import (
	"time"

	"eigenflux_server/pkg/config"
)

type FreshnessParams struct {
	Offset time.Duration
	Scale  time.Duration
	Decay  float64
}

type RankerConfig struct {
	Alpha            float64 // semantic similarity weight
	Beta             float64 // keyword overlap weight
	Gamma            float64 // freshness/urgency weight
	Delta            float64 // diversity penalty weight
	UrgencyBoost     float64
	UrgencyWindow    time.Duration
	MMRLambda        float64
	ExplorationSlots int
	DraftDampening      float64 // applied to draft items (default 0.8)
	MinRelevanceScore   float64 // items below this total score are dropped (default 0.1)
	EnableKNNRecall     bool
	KNNRecallK          int
	KNNRecallCandidates int
	Freshness           map[string]FreshnessParams // keyed by broadcast_type
}

func NewRankerConfig(cfg *config.Config) *RankerConfig {
	return &RankerConfig{
		Alpha:            cfg.ScoreWeightSemantic,
		Beta:             cfg.ScoreWeightKeyword,
		Gamma:            cfg.ScoreWeightFreshness,
		Delta:            cfg.ScoreWeightDiversity,
		UrgencyBoost:     cfg.UrgencyBoost,
		UrgencyWindow:    parseDuration(cfg.UrgencyWindow, 24*time.Hour),
		MMRLambda:        cfg.MMRLambda,
		ExplorationSlots: cfg.ExplorationSlots,
		DraftDampening:      0.8,
		MinRelevanceScore:   cfg.MinRelevanceScore,
		EnableKNNRecall:     cfg.EnableKNNRecall,
		KNNRecallK:          cfg.KNNRecallK,
		KNNRecallCandidates: cfg.KNNRecallCandidates,
		Freshness: map[string]FreshnessParams{
			"alert": {
				Offset: parseDuration(cfg.FreshnessAlertOffset, 2*time.Hour),
				Scale:  parseDuration(cfg.FreshnessAlertScale, 12*time.Hour),
				Decay:  cfg.FreshnessAlertDecay,
			},
			"demand": {
				Offset: parseDuration(cfg.FreshnessOffset, 12*time.Hour),
				Scale:  parseDuration(cfg.FreshnessScale, 7*24*time.Hour),
				Decay:  cfg.FreshnessDecay,
			},
			"info": {
				Offset: parseDuration(cfg.FreshnessOffset, 12*time.Hour),
				Scale:  parseDuration(cfg.FreshnessScale, 7*24*time.Hour),
				Decay:  cfg.FreshnessDecay,
			},
			"supply": {
				Offset: parseDuration(cfg.FreshnessSupplyOffset, 48*time.Hour),
				Scale:  parseDuration(cfg.FreshnessSupplyScale, 30*24*time.Hour),
				Decay:  cfg.FreshnessSupplyDecay,
			},
		},
	}
}

func parseDuration(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	// Handle "Nd" day notation (e.g. "30d", "7d")
	if len(s) > 1 && s[len(s)-1] == 'd' {
		var days int
		for _, ch := range s[:len(s)-1] {
			if ch < '0' || ch > '9' {
				return fallback
			}
			days = days*10 + int(ch-'0')
		}
		return time.Duration(days) * 24 * time.Hour
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}
