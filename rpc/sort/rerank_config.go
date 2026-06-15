package main

import (
	"context"
	"time"

	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/recallsource"
	sortDal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/rank"
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

func (l *rerankPolicySet) Policies() []rerank.Policy {
	if l == nil {
		return nil
	}
	return l.policies
}

type itemRerankSource struct {
	item sortDal.Item
}

func (s itemRerankSource) ItemFreshnessFields() (string, time.Time) {
	return s.item.Type, s.item.UpdatedAt
}

func applyItemRerankPolicies(ctx context.Context, items []sortDal.Item, sourceMap map[int64]recallsource.Source) []sortDal.Item {
	if itemRerankPolicies == nil {
		return items
	}
	policies := itemRerankPolicies.Policies()
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
