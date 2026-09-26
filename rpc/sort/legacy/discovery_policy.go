package legacy

import (
	"context"
	sortdal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/discovery"
	"eigenflux_server/rpc/sort/rank"
	"eigenflux_server/rpc/sort/rerank"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Existing broadcast policies apply only within one context and source kind,
// after hard constraints and the independent relevance/score gates.
func (s *Service) DiscoveryPolicies(ctx context.Context, in []discovery.Candidate, mode discovery.Mode, limit int) ([]discovery.Candidate, error) {
	buckets := map[string][]discovery.Candidate{}
	order := []string{}
	for _, c := range in {
		k := fmt.Sprintf("%d:%s", c.Context.ID, c.Document.Ref.Type)
		if _, ok := buckets[k]; !ok {
			order = append(order, k)
		}
		buckets[k] = append(buckets[k], c)
	}
	out := []discovery.Candidate{}
	for _, key := range order {
		pool := buckets[key]
		if pool[0].Document.Ref.Type != discovery.Broadcast || s.itemRerankPolicies == nil {
			out = append(out, pool...)
			continue
		}
		sort.SliceStable(pool, func(i, j int) bool {
			if pool[i].Score.Value != pool[j].Score.Value {
				return pool[i].Score.Value > pool[j].Score.Value
			}
			return pool[i].Document.Ref.ID < pool[j].Document.Ref.ID
		})
		sourceItems := []sortdal.Item{}
		for _, c := range pool {
			sourceItems = append(sourceItems, sortdal.Item{ID: c.Document.Ref.ID, AuthorAgentID: c.Document.AuthorID})
		}
		classes := s.resolveContentClasses(ctx, sourceItems)
		cs := []rank.Candidate{}
		byID := map[int64]discovery.Candidate{}
		ids := []int64{}
		for _, c := range pool {
			d := c.Document
			updated := d.SourceUpdatedAt
			source := itemRerankSource{contentClass: classes[d.Ref.ID], item: sortdal.Item{ID: d.Ref.ID, Type: d.ContentType, SourceType: d.SourceType, UpdatedAt: time.UnixMilli(updated)}}
			cs = append(cs, rank.NewCandidate(d.Ref.ID, rank.CandidateItem, c.Score.Value, c.Score.Features, source))
			byID[d.Ref.ID] = c
			ids = append(ids, d.Ref.ID)
		}
		for _, p := range s.itemRerankPolicies.PreRankPolicies() {
			cs = p.Apply(cs)
		}
		for _, p := range s.itemRerankPolicies.PostRankPolicies() {
			cs = p.Apply(cs)
		}
		ttl := time.Duration(0)
		if mode == discovery.Recommendation {
			claimed := fetchInjectClaims(ctx, s.redis, ids)
			for _, rule := range s.itemRerankPolicies.InjectRules() {
				rule := rule
				t, err := rule.ParsedClaimTTL()
				if err != nil {
					return nil, err
				}
				if t > ttl {
					ttl = t
				}
				cs = (&rerank.InjectPolicy{Match: func(c rank.Candidate) bool {
					return !claimed[c.ID()] && slices.Contains(byID[c.ID()].Document.Channels, rule.Source)
				}, Count: rule.Count, Positions: rule.Positions}).Apply(cs)
			}
		}
		for _, rule := range s.itemRerankPolicies.SourceLimits() {
			rule := rule
			cs = (&rerank.MatchLimitPolicy{Match: func(c rank.Candidate) bool { return slices.Contains(byID[c.ID()].Document.Channels, rule.Source) }, MaxCount: rule.MaxCount(limit), ReasonTag: "source=" + rule.Source}).Apply(cs)
		}
		for i, c := range cs {
			v := byID[c.ID()]
			v.Order = i + 1
			v.FinalScore = c.Score()
			if b, ok := c.(*rank.BasicCandidate); ok {
				v.Reasons = b.Reasons()
				for _, reason := range v.Reasons {
					if strings.HasPrefix(reason, "inject:") {
						v.ClaimTTLSeconds = int64(ttl / time.Second)
					}
				}
			}
			out = append(out, v)
		}
	}
	return out, nil
}
