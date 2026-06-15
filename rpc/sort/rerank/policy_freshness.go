package rerank

import (
	"time"

	"eigenflux_server/rpc/sort/rank"
)

type ItemFreshnessRule struct {
	BroadcastType string
	MaxAge        time.Duration
	Action        string
}

type FreshnessPolicy struct {
	ItemRules []ItemFreshnessRule
	Now       func() time.Time
}

func (p *FreshnessPolicy) Name() string { return "freshness" }

func (p *FreshnessPolicy) Apply(cands []rank.Candidate) []rank.Candidate {
	if len(cands) == 0 || len(p.ItemRules) == 0 {
		return cands
	}

	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}

	out := cands[:0]
	for _, c := range cands {
		if p.shouldDrop(c, now) {
			tagCandidate(c, "freshness:drop")
			continue
		}
		out = append(out, c)
	}
	return out
}

func (p *FreshnessPolicy) shouldDrop(c rank.Candidate, now time.Time) bool {
	if c.Type() != rank.CandidateItem {
		return false
	}
	fields, ok := itemTimeFields(c.Source())
	if !ok || fields.BroadcastType == "" || fields.UpdatedAt.IsZero() {
		return false
	}
	for _, rule := range p.ItemRules {
		if rule.Action != "drop" || rule.MaxAge <= 0 {
			continue
		}
		if rule.BroadcastType == fields.BroadcastType && now.Sub(fields.UpdatedAt) > rule.MaxAge {
			return true
		}
	}
	return false
}

type itemFreshnessFields interface {
	ItemFreshnessFields() (broadcastType string, updatedAt time.Time)
}

type freshnessFields struct {
	BroadcastType string
	UpdatedAt     time.Time
}

func itemTimeFields(src any) (freshnessFields, bool) {
	if src == nil {
		return freshnessFields{}, false
	}
	if v, ok := src.(itemFreshnessFields); ok {
		broadcastType, updatedAt := v.ItemFreshnessFields()
		return freshnessFields{BroadcastType: broadcastType, UpdatedAt: updatedAt}, true
	}
	return freshnessFields{}, false
}
