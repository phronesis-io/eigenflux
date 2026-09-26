package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
)

type Rule struct {
	Version      string  `json:"version"`
	BM25Scale    float64 `json:"bm25_scale"`
	CosineFloor  float64 `json:"cosine_floor"`
	MinRelevance float64 `json:"min_relevance"`
	Threshold    float64 `json:"threshold"`
	HalfLifeMS   int64   `json:"half_life_ms"`
}
type Rules map[Kind]map[Mode]Rule

func (rs Rules) Validate() error {
	for _, k := range AllKinds {
		for _, m := range []Mode{Search, Recommendation} {
			r, ok := rs[k][m]
			if !ok || r.Version == "" || !finite(r.BM25Scale) || r.BM25Scale <= 0 || !finite(r.CosineFloor) || r.CosineFloor < -1 || r.CosineFloor >= 1 || !finite(r.MinRelevance) || r.MinRelevance < 0 || r.MinRelevance > 1 || !finite(r.Threshold) || r.Threshold < 0 || r.Threshold > 1 || r.HalfLifeMS <= 0 {
				return Invalid("rules", "invalid_kind_mode_config")
			}
		}
	}
	return nil
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func clamp(v float64) float64 {
	if !finite(v) {
		return 0
	}
	return math.Max(0, math.Min(1, v))
}
func Cosine(a, b []float32) (float64, bool) {
	if len(a) == 0 || len(a) != len(b) {
		return 0, false
	}
	var dot, aa, bb float64
	for i, x := range a {
		y := b[i]
		if !finite(float64(x)) || !finite(float64(y)) {
			return 0, false
		}
		dot += float64(x) * float64(y)
		aa += float64(x) * float64(x)
		bb += float64(y) * float64(y)
	}
	if aa == 0 || bb == 0 {
		return 0, false
	}
	return math.Max(-1, math.Min(1, dot/math.Sqrt(aa*bb))), true
}
func decay(ts, now, half int64) float64 {
	if ts <= 0 {
		return 0
	}
	return math.Exp2(-math.Max(0, float64(now-ts)) / float64(half))
}
func ScoreRules(c Context, d Document, rule Rule, now int64) Score {
	raw, _ := json.Marshal(rule)
	sum := sha256.Sum256(raw)
	s := Score{Threshold: rule.Threshold, MinRelevance: rule.MinRelevance, RequestTime: now, Contributions: map[string]float64{}, ScorerType: "rules", Version: rule.Version, Kind: "rule_score", ConfigHash: hex.EncodeToString(sum[:]), Features: map[string]float64{}}
	if d.Ref.Type == Agent && c.Origin == "query" && d.ExactMatch != "" {
		s.Value, s.Relevance, s.Eligible = 1, 1, true
		s.Version, s.Kind = "agent_identity_v1", "exact_match"
		s.Features["exact_match"] = 1
		s.Contributions["exact_match"] = 1
		return s
	}
	lex := 0.0
	if d.Lexical > 0 {
		lex = d.Lexical / (d.Lexical + rule.BM25Scale)
	} else {
		s.Missing = append(s.Missing, "lexical")
	}
	semantic := 0.0
	if cos, ok := Cosine(c.Vector, d.Vector); ok {
		s.Features["cosine"] = cos
		semantic = clamp((cos - rule.CosineFloor) / (1 - rule.CosineFloor))
	} else {
		s.Missing = append(s.Missing, "semantic")
	}
	slot := 0.0
	if c.Filters.TaxonomyVersion != "" && c.Filters.TaxonomyVersion == d.Slots.TaxonomyVersion {
		if c.Filters.Category != "" && c.Filters.Category == d.Slots.Category {
			slot += .15
		}
		if c.Filters.Subtype != "" && c.Filters.Subtype == d.Slots.Subtype {
			slot += .25
		}
	}
	if c.TaxonomyVersion == d.Slots.TaxonomyVersion && len(c.SoftIntents) > 0 {
		n := 0
		for _, id := range c.SoftIntents {
			if intersects([]string{id}, d.Slots.Intents) {
				n++
			}
		}
		slot += .60 * float64(n) / float64(len(c.SoftIntents))
	}
	s.Relevance = math.Max(slot, .55*lex+.45*semantic)
	fresh := decay(d.FreshAt, now, rule.HalfLifeMS)
	quality := clamp(d.Quality)
	s.Features["slot"] = slot
	s.Features["lexical"] = lex
	s.Features["semantic"] = semantic
	s.Features["freshness"] = fresh
	s.Features["quality"] = quality
	switch d.Ref.Type {
	case Broadcast:
		s.Contributions = map[string]float64{"relevance": .85 * s.Relevance, "freshness": .10 * fresh, "quality": .05 * quality}
		s.Value = .85*s.Relevance + .10*fresh + .05*quality
	case Commission:
		slack := 0.0
		if c.Filters.BudgetMaxFen != nil && d.PriceFen != nil {
			if *c.Filters.BudgetMaxFen > 0 {
				slack = clamp(1 - float64(*d.PriceFen)/float64(*c.Filters.BudgetMaxFen))
			} else if *d.PriceFen == 0 {
				slack = 1
			}
		}
		s.Features["budget_slack"] = slack
		s.Features["fulfillment"] = clamp(d.Fulfillment)
		s.Contributions = map[string]float64{"relevance": .85 * s.Relevance, "fulfillment": .10 * clamp(d.Fulfillment), "budget_slack": .05 * slack}
		s.Value = .85*s.Relevance + .10*clamp(d.Fulfillment) + .05*slack
	case Agent:
		activity := decay(d.ActivityAt, now, rule.HalfLifeMS)
		s.Features["activity_freshness"] = activity
		s.Contributions = map[string]float64{"relevance": .90 * s.Relevance, "activity_freshness": .10 * activity}
		s.Value = .90*s.Relevance + .10*activity
	}
	s.Eligible = s.Relevance >= rule.MinRelevance && s.Value >= rule.Threshold
	if c.Origin == "baseline" {
		s.Version = "baseline_quality_v1"
		s.Kind = "baseline_score"
		s.Contributions = map[string]float64{"freshness": .8 * fresh, "quality": .2 * quality}
		s.Value = .8*fresh + .2*quality
		s.Eligible = d.Ref.Type == Broadcast
	}
	return s
}

// Merge places exact search hits first, then interleaves ordinary hits in the
// requested kind order without comparing cross-kind scores. Automatic selection
// uses context priority.
func Merge(in []Candidate, kinds []Kind, mode Mode, limit int) []Candidate {
	in = append([]Candidate(nil), in...)
	sort.SliceStable(in, func(i, j int) bool {
		a, b := in[i], in[j]
		if a.Context.Priority != b.Context.Priority {
			return a.Context.Priority > b.Context.Priority
		}
		if mode == Recommendation && a.Context.UpdatedAt != b.Context.UpdatedAt {
			return a.Context.UpdatedAt > b.Context.UpdatedAt
		}
		if a.Context.ID != b.Context.ID && mode == Recommendation {
			return a.Context.ID < b.Context.ID
		}
		if a.Document.Ref.Type != b.Document.Ref.Type {
			return kindPosition(kinds, a.Document.Ref.Type) < kindPosition(kinds, b.Document.Ref.Type)
		}
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		if a.Score.Value != b.Score.Value {
			return a.Score.Value > b.Score.Value
		}
		return a.Document.Ref.ID < b.Document.Ref.ID
	})
	out := make([]Candidate, 0)
	seen := map[string]bool{}
	groups := map[int64]bool{}
	add := func(c Candidate) {
		key := c.Document.Ref.Key()
		if !c.Score.Eligible || seen[key] || c.Document.Ref.Type == Broadcast && c.Document.GroupID > 0 && groups[c.Document.GroupID] {
			return
		}
		seen[key] = true
		if c.Document.Ref.Type == Broadcast && c.Document.GroupID > 0 {
			groups[c.Document.GroupID] = true
		}
		out = append(out, c)
	}
	if mode == Recommendation {
		for _, c := range in {
			add(c)
			if len(out) == limit {
				break
			}
		}
		return out
	}
	buckets := map[Kind][]Candidate{}
	for _, c := range in {
		if c.Document.ExactMatch != "" {
			if len(out) >= limit {
				return out
			}
			if hasKind(kinds, c.Document.Ref.Type) {
				add(c)
			}
			continue
		}
		buckets[c.Document.Ref.Type] = append(buckets[c.Document.Ref.Type], c)
	}
	for len(out) < limit {
		progress := false
		for _, k := range kinds {
			if len(buckets[k]) > 0 {
				c := buckets[k][0]
				buckets[k] = buckets[k][1:]
				add(c)
				progress = true
				if len(out) == limit {
					break
				}
			}
		}
		if !progress {
			break
		}
	}
	return out
}
func kindPosition(kinds []Kind, k Kind) int {
	for i, x := range kinds {
		if x == k {
			return i
		}
	}
	return len(kinds)
}
