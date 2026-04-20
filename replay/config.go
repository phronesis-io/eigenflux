package main

import (
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/rpc/sort/ranker"
)

type ReplayRankerParams struct {
	Alpha             *float64                         `json:"alpha,omitempty"`
	Beta              *float64                         `json:"beta,omitempty"`
	Gamma             *float64                         `json:"gamma,omitempty"`
	Delta             *float64                         `json:"delta,omitempty"`
	MinRelevanceScore *float64                         `json:"min_relevance_score,omitempty"`
	UrgencyBoost      *float64                         `json:"urgency_boost,omitempty"`
	UrgencyWindow     *string                          `json:"urgency_window,omitempty"`
	ExplorationSlots  *int                             `json:"exploration_slots,omitempty"`
	DraftDampening    *float64                         `json:"draft_dampening,omitempty"`
	Freshness         map[string]ReplayFreshnessParams `json:"freshness,omitempty"`
}

type ReplayFreshnessParams struct {
	Offset *string  `json:"offset,omitempty"`
	Scale  *string  `json:"scale,omitempty"`
	Decay  *float64 `json:"decay,omitempty"`
}

type ReplayRecallParams struct {
	KeywordRecallSize   *int  `json:"keyword_recall_size,omitempty"`
	EnableKNNRecall     *bool `json:"enable_knn_recall,omitempty"`
	KNNRecallK          *int  `json:"knn_recall_k,omitempty"`
	KNNRecallCandidates *int  `json:"knn_recall_candidates,omitempty"`
}

func mergeRankerConfig(base *ranker.RankerConfig, override *ReplayRankerParams) *ranker.RankerConfig {
	merged := *base
	if override == nil {
		return &merged
	}
	if override.Alpha != nil {
		merged.Alpha = *override.Alpha
	}
	if override.Beta != nil {
		merged.Beta = *override.Beta
	}
	if override.Gamma != nil {
		merged.Gamma = *override.Gamma
	}
	if override.Delta != nil {
		merged.Delta = *override.Delta
	}
	if override.MinRelevanceScore != nil {
		merged.MinRelevanceScore = *override.MinRelevanceScore
	}
	if override.UrgencyBoost != nil {
		merged.UrgencyBoost = *override.UrgencyBoost
	}
	if override.UrgencyWindow != nil {
		merged.UrgencyWindow = parseDurationWithDefault(*override.UrgencyWindow, merged.UrgencyWindow)
	}
	if override.ExplorationSlots != nil {
		merged.ExplorationSlots = *override.ExplorationSlots
	}
	if override.DraftDampening != nil {
		merged.DraftDampening = *override.DraftDampening
	}
	if override.Freshness != nil {
		freshness := make(map[string]ranker.FreshnessParams)
		for k, v := range merged.Freshness {
			freshness[k] = v
		}
		for k, v := range override.Freshness {
			fp := freshness[k]
			if v.Offset != nil {
				fp.Offset = parseDurationWithDefault(*v.Offset, fp.Offset)
			}
			if v.Scale != nil {
				fp.Scale = parseDurationWithDefault(*v.Scale, fp.Scale)
			}
			if v.Decay != nil {
				fp.Decay = *v.Decay
			}
			freshness[k] = fp
		}
		merged.Freshness = freshness
	}
	return &merged
}

func mergeRecallParams(cfg *config.Config, override *ReplayRecallParams) (keywordRecallSize int, enableKNN bool, knnK int, knnCandidates int) {
	keywordRecallSize = cfg.KeywordRecallSize
	enableKNN = cfg.EnableKNNRecall
	knnK = cfg.KNNRecallK
	knnCandidates = cfg.KNNRecallCandidates
	if override == nil {
		return
	}
	if override.KeywordRecallSize != nil {
		keywordRecallSize = *override.KeywordRecallSize
	}
	if override.EnableKNNRecall != nil {
		enableKNN = *override.EnableKNNRecall
	}
	if override.KNNRecallK != nil {
		knnK = *override.KNNRecallK
	}
	if override.KNNRecallCandidates != nil {
		knnCandidates = *override.KNNRecallCandidates
	}
	return
}

func parseDurationWithDefault(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
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
