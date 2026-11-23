package discovery

import (
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"eigenflux_server/pkg/metrics"
	"eigenflux_server/rpc/sort/discoverylr"
)

const gbdtSalt = "discovery-gbdt-v1:"

func snapshotVector(c Candidate) ([]float64, bool) {
	d, s := c.Document, c.Score
	if d.Ref.Type != Broadcast || s.RequestTime < 0 || d.FreshAt <= 0 || d.ExpiresAt < 0 {
		return nil, false
	}
	v := make([]float64, 0, len(discoverylr.SnapshotFeatureNames))
	for _, name := range discoverylr.SnapshotFeatureNames[:4] {
		x, ok := s.Features[name]
		if !ok || !finite(x) {
			return nil, false
		}
		v = append(v, clamp(x))
	}
	if !finite(s.Relevance) || !finite(s.Value) {
		return nil, false
	}
	v = append(v, clamp(s.Relevance), clamp(s.Value))
	flag := func(b bool) {
		if b {
			v = append(v, 1)
		} else {
			v = append(v, 0)
		}
	}
	flag(slices.Contains(s.Missing, "semantic"))
	// Python str.split also treats U+001C through U+001F as whitespace.
	words := strings.FieldsFunc(c.Context.Query, func(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) })
	length := len(words) - 1
	for _, w := range words {
		length += utf8.RuneCountInString(w)
	}
	flag(len(words) > 0)
	scaled := func(x, cap float64) float64 { return math.Log1p(max(0, min(x, cap))) / math.Log1p(cap) }
	v = append(v, scaled(float64(length), 2000), scaled(float64(s.RequestTime-d.FreshAt)/3600000, 2160), scaled(float64(d.ExpiresAt-s.RequestTime)/3600000, 2160))
	flag(d.ExpiresAt > 0)
	flag(slices.ContainsFunc(c.Context.Filters.Lang, func(lang string) bool { return slices.Contains(d.Slots.Lang, lang) }))
	flag(len(c.Context.Filters.Lang) > 0)
	category := func(value string, known ...string) {
		for _, k := range known {
			flag(value == k)
		}
		flag(!slices.Contains(known, value))
	}
	category(c.Context.Origin, "query", "need_input", "agent_context", "baseline", "friend")
	category(d.ContentType, "info", "alert", "supply", "demand")
	category(d.SourceType, "original", "curated", "forwarded")
	return v, true
}

func rankSnapshot(model LearnedRanker, candidates []Candidate) bool {
	var indices []int
	var vectors [][]float64
	for i, c := range candidates {
		if c.Document.Ref.Type != Broadcast || !c.Score.Eligible {
			continue
		}
		v, ok := snapshotVector(c)
		if !ok {
			metrics.DiscoveryLRScoring.WithLabelValues("gbdt_invalid_features").Inc()
			return false
		}
		indices = append(indices, i)
		vectors = append(vectors, v)
	}
	if len(indices) == 0 {
		return false
	}
	scores, ok := model.ScoreBatch(vectors)
	if !ok || !validBatchScores(scores, len(indices), discoverylr.SnapshotContract) {
		metrics.DiscoveryLRScoring.WithLabelValues("gbdt_fallback").Inc()
		return false
	}
	for j, i := range indices {
		candidates[i].LR = &scores[j]
		candidates[i].FinalScore = scores[j].Probability
	}
	metrics.DiscoveryLRScoring.WithLabelValues("gbdt_scored").Inc()
	return true
}
