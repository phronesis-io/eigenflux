package lrranker

import (
	"eigenflux_server/pkg/tagnorm"
)

// buildRaw reproduces features.decode_raw_features: it turns one Input into the
// map of raw feature values keyed by term source. Values are typed by kind:
//   - numeric sources -> *float64 (nil == Python None, filled to 0 at vectorize)
//   - bool sources     -> bool
//   - enum sources      -> normalized string
//   - "recall_source"   -> set of normalized recall names
//
// tagnorm.Normalize is the same separator/lowercase folding the training side
// documents itself as matching (features.normalize_tag).
func buildRaw(in Input) map[string]any {
	profileKw := normalizedTagSet(in.AgentKeywords)
	profileDom := normalizedTagSet(in.AgentDomains)
	itemKw := normalizedTagSet(in.ItemKeywords)
	itemDom := normalizedTagSet(in.ItemDomains)

	kwInter, kwOverlap := overlap(profileKw, itemKw)
	domInter, domOverlap := overlap(profileDom, itemDom)

	agentGeo := tagnorm.Normalize(in.AgentGeo)
	itemGeo := tagnorm.Normalize(in.ItemGeo)

	// Python guards age/expire with `if created_at:` / `if expire_time:`, which
	// is false for both None and 0. has_expire_time / missing.created_at use the
	// None check only, so a literal 0 timestamp still counts as "present".
	ageHours := 0.0
	if in.CreatedAtMS != nil && *in.CreatedAtMS != 0 {
		ageHours = maxFloat(0, float64(in.ServedAtMS-*in.CreatedAtMS)/3_600_000)
	}
	expireHours := 0.0
	if in.ExpireTimeMS != nil && *in.ExpireTimeMS != 0 {
		expireHours = maxFloat(0, float64(*in.ExpireTimeMS-in.ServedAtMS)/3_600_000)
	}

	raw := map[string]any{
		"baseline.semantic":  in.RankScores.Semantic,
		"baseline.keyword":   in.RankScores.Keyword,
		"baseline.freshness": in.RankScores.Freshness,
		"baseline.total":     in.RankScores.Total,
		"content.quality":    in.QualityScore,

		"time.age_hours":             ptr(ageHours),
		"time.time_to_expire_hours":  ptr(expireHours),
		"cross.keyword_intersection": ptr(float64(kwInter)),
		"cross.keyword_overlap":      ptr(kwOverlap),
		"cross.domain_intersection":  ptr(float64(domInter)),
		"cross.domain_overlap":       ptr(domOverlap),

		"time.has_expire_time":  in.ExpireTimeMS != nil,
		"cross.geo_match":       agentGeo != "" && itemGeo != "" && agentGeo == itemGeo,
		"availability.is_draft": in.RankScores.IsDraft,

		"missing.baseline.semantic":  in.RankScores.Semantic == nil,
		"missing.baseline.keyword":   in.RankScores.Keyword == nil,
		"missing.baseline.freshness": in.RankScores.Freshness == nil,
		"missing.baseline.total":     in.RankScores.Total == nil,
		"missing.content.quality":    in.QualityScore == nil,
		"missing.time.created_at":    in.CreatedAtMS == nil,

		"broadcast_type": normalizeEnum(in.BroadcastType, broadcastTypes, false),
		"source_type":    normalizeEnum(in.SourceType, sourceTypes, false),
		"timeliness":     normalizeEnum(in.Timeliness, timelinessVals, false),
		"lang":           normalizeEnum(in.Lang, langVals, true),

		"recall_source": recallNameSet(in.RecallSourceNames),
	}
	return raw
}

// normalizedTagSet returns the deduplicated set of tagnorm-normalized tags,
// dropping empties — matching features._tags.
func normalizedTagSet(tags []string) map[string]bool {
	set := make(map[string]bool, len(tags))
	for _, t := range tags {
		if n := tagnorm.Normalize(t); n != "" {
			set[n] = true
		}
	}
	return set
}

// overlap returns (|profile ∩ item|, |profile ∩ item| / |item|), matching
// features._overlap. Ratio denominator is the item set size (0 when empty).
func overlap(profile, item map[string]bool) (int, float64) {
	if len(item) == 0 {
		return 0, 0.0
	}
	inter := 0
	for k := range item {
		if profile[k] {
			inter++
		}
	}
	return inter, float64(inter) / float64(len(item))
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func ptr(f float64) *float64 { return &f }
