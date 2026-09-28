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
	if (d.Ref.Type == Agent || d.Ref.Type == Commission) && c.Origin == "query" && d.ExactMatch != "" {
		s.Value, s.Relevance, s.Eligible = 1, 1, true
		s.Version, s.Kind = string(d.Ref.Type)+"_identity_v1", "exact_match"
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
	cos, ok := 0.0, false
	if d.DenseScore != nil && finite(*d.DenseScore) && *d.DenseScore >= 0 && *d.DenseScore <= 1 {
		// ES cosine kNN returns (1 + cosine) / 2, without lexical boosts.
		cos, ok = 2*(*d.DenseScore)-1, true
	}
	if ok {
		s.Features["cosine"] = cos
		semantic = clamp((cos - rule.CosineFloor) / (1 - rule.CosineFloor))
	} else {
		s.Missing = append(s.Missing, "semantic")
	}
	s.Relevance = lex
	if ok {
		s.Relevance = .55*lex + .45*semantic
	}
	fresh := decay(d.FreshAt, now, rule.HalfLifeMS)
	quality := clamp(d.Quality)
	s.Features["lexical"] = lex
	s.Features["semantic"] = semantic
	s.Features["freshness"] = fresh
	s.Features["quality"] = quality
	switch d.Ref.Type {
	case Broadcast:
		s.Version = rule.Version + ":broadcast_dense_v1"
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

// Merge selects search hits round-robin in requested kind order, with exact
// hits first within each kind. Automatic selection uses context priority.
// The engine groups each selected page by kind before freezing or delivering it.
func Merge(in []Candidate, kinds []Kind, mode Mode, limit int) []Candidate {
	if limit <= 0 {
		return []Candidate{}
	}
	in = append([]Candidate(nil), in...)
	sort.SliceStable(in, func(i, j int) bool {
		a, b := in[i], in[j]
		if mode == Recommendation && (a.Context.CapturedNeed != nil) != (b.Context.CapturedNeed != nil) {
			return a.Context.CapturedNeed != nil
		}
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
	// Stable partition before deduplication preserves exact provenance when a
	// source is present in both exact and ordinary recall.
	for _, exact := range []bool{true, false} {
		for _, c := range in {
			if (c.Document.ExactMatch != "") == exact {
				buckets[c.Document.Ref.Type] = append(buckets[c.Document.Ref.Type], c)
			}
		}
	}
	for len(out) < limit {
		progress := false
		for _, k := range kinds {
			for len(buckets[k]) > 0 {
				before := len(out)
				c := buckets[k][0]
				buckets[k] = buckets[k][1:]
				add(c)
				progress = true
				if len(out) > before {
					break
				}
			}
			if len(out) == limit {
				break
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

// groupResultPages changes presentation only: each page keeps its selected
// membership and within-kind ranking. Grouping before pagination would let a
// large first kind monopolize the first pages. Sort freezes this final order so
// response items, cursors and replay positions all agree.
func groupResultPages(in []Candidate, kinds []Kind, pageSize int) {
	if pageSize <= 0 {
		return
	}
	for start := 0; start < len(in); start += pageSize {
		page := in[start:min(start+pageSize, len(in))]
		sort.SliceStable(page, func(i, j int) bool {
			a, b := page[i].Document, page[j].Document
			if a.Ref.Type != b.Ref.Type {
				return kindPosition(kinds, a.Ref.Type) < kindPosition(kinds, b.Ref.Type)
			}
			return a.ExactMatch != "" && b.ExactMatch == ""
		})
	}
}
