package lrranker

import (
	"math"
	"strings"
)

// RankScores are the baseline formula ranker outputs, reused as LR features.
// Pointers distinguish "missing" (nil) from a real 0, matching the Python
// missing.* indicators: a nil field sets its missing.baseline.* term to 1.
type RankScores struct {
	Semantic  *float64
	Keyword   *float64
	Freshness *float64
	Total     *float64
	IsDraft   bool
}

// Input is the neutral scoring input shared by the online path and the golden
// self-tests. Both extract these primitives and feed the single buildRaw so the
// two paths cannot diverge. Nullable numbers use pointers to reproduce Python's
// None-vs-0 semantics exactly.
type Input struct {
	ServedAtMS int64

	AgentKeywords []string
	AgentDomains  []string
	AgentGeo      string

	ItemKeywords      []string
	ItemDomains       []string
	ItemGeo           string
	BroadcastType     string
	SourceType        string
	Timeliness        string
	Lang              string
	QualityScore      *float64
	CreatedAtMS       *int64
	ExpireTimeMS      *int64
	RecallSourceNames []string
	RankScores        RankScores
}

// numberOf reproduces Python features._number: bool is treated as None,
// non-finite is None, and only genuine finite numbers pass through.
func numberOf(v any) *float64 {
	switch n := v.(type) {
	case bool:
		return nil
	case float64:
		if math.IsInf(n, 0) || math.IsNaN(n) {
			return nil
		}
		return &n
	case float32:
		f := float64(n)
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return nil
		}
		return &f
	case int:
		f := float64(n)
		return &f
	case int64:
		f := float64(n)
		return &f
	default:
		return nil
	}
}

// timestampMS reproduces Python features._timestamp_ms for the JSON self-test
// path: a finite number becomes its int form; anything else is None. ISO-string
// timestamps are not produced by the online path and are not needed by the
// bundled fixtures, so only the numeric form is supported.
func timestampMS(v any) *int64 {
	if n := numberOf(v); n != nil {
		ms := int64(*n)
		return &ms
	}
	return nil
}

// normalizeEnum mirrors features.normalize_enum: lowercase+trim, optional lang
// prefix split on '-'/'_', and fold to the trailing "other" bucket when the
// value is not one of the allowed non-other categories.
func normalizeEnum(value string, allowed []string, lang bool) string {
	n := strings.ToLower(strings.TrimSpace(value))
	if lang && n != "" {
		if i := strings.IndexByte(n, '-'); i >= 0 {
			n = n[:i]
		}
		if i := strings.IndexByte(n, '_'); i >= 0 {
			n = n[:i]
		}
	}
	for _, a := range allowed[:len(allowed)-1] {
		if n == a {
			return n
		}
	}
	return "other"
}

// recallNameSet mirrors features._names ∩ RECALL_SOURCES: recall source names
// are only stripped+lowercased (NOT tag-normalized) before intersecting the
// fixed vocabulary.
func recallNameSet(names []string) map[string]bool {
	allowed := make(map[string]bool, len(recallSources))
	for _, s := range recallSources {
		allowed[s] = true
	}
	out := make(map[string]bool, len(names))
	for _, raw := range names {
		n := strings.ToLower(strings.TrimSpace(raw))
		if n != "" && allowed[n] {
			out[n] = true
		}
	}
	return out
}

// stringSlice extracts a []string from a decoded-JSON value ([]any of strings).
func stringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func stringOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// DecodeSelfTestInput converts a bundle self_test_cases[].input JSON object
// (served_at, agent_features_json, item_features_json) into an Input, applying
// the same _number/_timestamp_ms coercions the Python decoder uses. This is the
// parity entry point used by validate-on-load and the cross-language golden test.
func DecodeSelfTestInput(raw map[string]any) Input {
	agent, _ := raw["agent_features_json"].(map[string]any)
	item, _ := raw["item_features_json"].(map[string]any)
	if agent == nil {
		agent = map[string]any{}
	}
	if item == nil {
		item = map[string]any{}
	}
	rank, _ := item["rank_scores"].(map[string]any)
	if rank == nil {
		rank = map[string]any{}
	}

	served := int64(0)
	if s := timestampMS(raw["served_at"]); s != nil {
		served = *s
	}

	isDraft, _ := rank["is_draft"].(bool)

	return Input{
		ServedAtMS:        served,
		AgentKeywords:     stringSlice(agent["keywords"]),
		AgentDomains:      stringSlice(agent["domains"]),
		AgentGeo:          stringOf(agent["geo"]),
		ItemKeywords:      stringSlice(item["keywords"]),
		ItemDomains:       stringSlice(item["domains"]),
		ItemGeo:           stringOf(item["geo"]),
		BroadcastType:     stringOf(item["broadcast_type"]),
		SourceType:        stringOf(item["source_type"]),
		Timeliness:        stringOf(item["timeliness"]),
		Lang:              stringOf(item["lang"]),
		QualityScore:      numberOf(item["quality_score"]),
		CreatedAtMS:       timestampMS(item["created_at"]),
		ExpireTimeMS:      timestampMS(item["expire_time"]),
		RecallSourceNames: stringSlice(item["recall_source_names"]),
		RankScores: RankScores{
			Semantic:  numberOf(rank["semantic"]),
			Keyword:   numberOf(rank["keyword"]),
			Freshness: numberOf(rank["freshness"]),
			Total:     numberOf(rank["total"]),
			IsDraft:   isDraft,
		},
	}
}
