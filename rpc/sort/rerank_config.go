package main

import (
	"context"
	"time"

	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/recallsource"
	sortDal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/rank"
	"eigenflux_server/rpc/sort/ranker"
	"eigenflux_server/rpc/sort/rerank"
)

type rerankPolicySet struct {
	policies []rerank.Policy
}

func loadRerankPolicySet(ctx context.Context, path string, now func() time.Time) *rerankPolicySet {
	if path == "" {
		return &rerankPolicySet{}
	}
	cfg, err := rerank.LoadConfig(path)
	if err != nil {
		logger.Ctx(ctx).Warn("rerank config load failed; continuing without configured policies", "path", path, "err", err)
		return &rerankPolicySet{}
	}
	if now == nil {
		now = time.Now
	}
	policies, err := cfg.NewPolicies(now)
	if err != nil {
		logger.Ctx(ctx).Warn("rerank config invalid; continuing without configured policies", "path", path, "err", err)
		return &rerankPolicySet{}
	}
	logger.Ctx(ctx).Info("rerank config loaded", "path", path, "policies", len(policies))
	return &rerankPolicySet{policies: policies}
}

// PreRankPolicies returns policies that run on recall candidates before item
// ranking (currently freshness drops). PostRankPolicies returns policies that
// run on ranked items so their score edits survive ranking (currently boosts).
func (l *rerankPolicySet) PreRankPolicies() []rerank.Policy {
	return l.filter(func(p rerank.Policy) bool {
		_, ok := p.(*rerank.FreshnessPolicy)
		return ok
	})
}

func (l *rerankPolicySet) PostRankPolicies() []rerank.Policy {
	return l.filter(func(p rerank.Policy) bool {
		_, ok := p.(*rerank.BoostPolicy)
		return ok
	})
}

func (l *rerankPolicySet) filter(keep func(rerank.Policy) bool) []rerank.Policy {
	if l == nil {
		return nil
	}
	out := make([]rerank.Policy, 0, len(l.policies))
	for _, p := range l.policies {
		if keep(p) {
			out = append(out, p)
		}
	}
	return out
}

type itemRerankSource struct {
	item sortDal.Item
	// contentClass is "ugc"/"pgc" resolved at request time from the author's
	// email suffix (empty for the pre-rank freshness path, which never reads it).
	contentClass string
}

func (s itemRerankSource) ItemFreshnessFields() (string, time.Time) {
	return s.item.Type, s.item.UpdatedAt
}

func (s itemRerankSource) ItemBoostFields() (string, string, string) {
	return s.item.Type, s.item.SourceType, s.contentClass
}

func applyItemRerankPolicies(ctx context.Context, items []sortDal.Item, sourceMap map[int64]recallsource.Source) []sortDal.Item {
	if itemRerankPolicies == nil {
		return items
	}
	policies := itemRerankPolicies.PreRankPolicies()
	if len(items) == 0 || len(policies) == 0 {
		return items
	}

	candidates := make([]rank.Candidate, 0, len(items))
	for i := range items {
		candidates = append(candidates, rank.NewCandidate(
			items[i].ID,
			rank.CandidateItem,
			items[i].Score,
			nil,
			itemRerankSource{item: items[i]},
		))
	}

	final := rerank.New(policies...).Rerank(candidates, 0)
	if len(final) == len(items) {
		return items
	}

	out := make([]sortDal.Item, 0, len(final))
	kept := make(map[int64]struct{}, len(final))
	for _, c := range final {
		src, ok := c.Source().(itemRerankSource)
		if !ok {
			continue
		}
		out = append(out, src.item)
		kept[src.item.ID] = struct{}{}
	}
	for _, item := range items {
		if _, ok := kept[item.ID]; !ok {
			delete(sourceMap, item.ID)
		}
	}
	logger.Ctx(ctx).Debug("item rerank policies applied", "before", len(items), "after", len(out), "dropped", len(items)-len(out))
	return out
}

// applyPostRankBoost multiplies ranked item scores by operator-tuned boost
// weights (supply/demand and UGC promotion) and returns the items re-sorted by
// boosted score. It runs after ranking so the score edits survive, and before
// the relevance threshold split so a boosted item can cross into the served set.
// Only RankedItem.Score is rewritten; the per-signal breakdown is left intact.
func applyPostRankBoost(ctx context.Context, ranked []ranker.RankedItem, itemMap map[int64]sortDal.Item, contentClassByItem map[int64]string) []ranker.RankedItem {
	if itemRerankPolicies == nil {
		return ranked
	}
	policies := itemRerankPolicies.PostRankPolicies()
	if len(ranked) == 0 || len(policies) == 0 {
		return ranked
	}

	byID := make(map[int64]ranker.RankedItem, len(ranked))
	candidates := make([]rank.Candidate, 0, len(ranked))
	for _, ri := range ranked {
		byID[ri.ItemID] = ri
		candidates = append(candidates, rank.NewCandidate(
			ri.ItemID,
			rank.CandidateItem,
			ri.Score,
			nil,
			itemRerankSource{item: itemMap[ri.ItemID], contentClass: contentClassByItem[ri.ItemID]},
		))
	}

	final := rerank.New(policies...).Rerank(candidates, 0)

	out := make([]ranker.RankedItem, 0, len(final))
	for _, c := range final {
		ri, ok := byID[c.ID()]
		if !ok {
			continue
		}
		ri.Score = c.Score()
		out = append(out, ri)
	}
	logger.Ctx(ctx).Debug("post-rank boost applied", "items", len(out))
	return out
}
