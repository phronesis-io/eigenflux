package rerank

import (
	"sort"

	"eigenflux_server/rpc/sort/rank"
)

// BoostRule multiplies the score of an item candidate whose Field value is in
// Values by Weight. Field is one of "type" (broadcast_type), "source_type", or
// "content_class" (ugc/pgc, derived from the author's email suffix).
type BoostRule struct {
	Field  string
	Values []string
	Weight float64
}

// BoostPolicy applies multiplicative score boosts to item candidates based on
// operator-tuned category rules (supply/demand promotion, UGC promotion). When
// several rules match one candidate their weights compound. Only *BasicCandidate
// scores are mutated; other Candidate implementations pass through untouched.
// The returned slice is re-sorted by descending score so callers can read the
// new display order directly.
type BoostPolicy struct {
	Rules []BoostRule
}

func (p *BoostPolicy) Name() string { return "boost" }

func (p *BoostPolicy) Apply(cands []rank.Candidate) []rank.Candidate {
	if len(cands) == 0 || len(p.Rules) == 0 {
		return cands
	}

	for _, c := range cands {
		if c.Type() != rank.CandidateItem {
			continue
		}
		bc, ok := c.(*rank.BasicCandidate)
		if !ok {
			continue
		}
		fields, ok := itemBoostFields(bc.Source())
		if !ok {
			continue
		}
		weight := 1.0
		for _, rule := range p.Rules {
			if rule.Weight <= 0 {
				continue
			}
			value := fields.value(rule.Field)
			if value == "" {
				continue
			}
			if containsValue(rule.Values, value) {
				weight *= rule.Weight
				bc.AddReason("boost:" + rule.Field + "=" + value)
			}
		}
		if weight != 1.0 {
			bc.SetScore(bc.Score() * weight)
		}
	}

	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].Score() > cands[j].Score()
	})
	return cands
}

func containsValue(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

// itemBoostFields is implemented by rerank sources that can expose the category
// fields a BoostPolicy reads. Mirrors the itemFreshnessFields pattern.
type itemBoostFieldsProvider interface {
	ItemBoostFields() (broadcastType string, sourceType string, contentClass string)
}

type boostFields struct {
	BroadcastType string
	SourceType    string
	ContentClass  string
}

func (f boostFields) value(field string) string {
	switch field {
	case "type":
		return f.BroadcastType
	case "source_type":
		return f.SourceType
	case "content_class":
		return f.ContentClass
	default:
		return ""
	}
}

func itemBoostFields(src any) (boostFields, bool) {
	if src == nil {
		return boostFields{}, false
	}
	if v, ok := src.(itemBoostFieldsProvider); ok {
		bt, st, cc := v.ItemBoostFields()
		return boostFields{BroadcastType: bt, SourceType: st, ContentClass: cc}, true
	}
	return boostFields{}, false
}
