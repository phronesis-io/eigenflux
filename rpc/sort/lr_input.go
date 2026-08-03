package main

import (
	"time"

	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/recallsource"
	sortDal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/lrranker"
	"eigenflux_server/rpc/sort/ranker"
)

// scoreItemsWithLR scores every eligible item with the live LR model and returns
// itemID -> result. It returns nil when the LR ranker is unavailable (disabled,
// no model loaded) or if the model becomes unavailable mid-request, signaling
// the caller to keep the baseline formula ordering. All candidates are scored
// from in-memory objects only; no per-item I/O is performed.
func scoreItemsWithLR(ranked []ranker.RankedItem, itemMap map[int64]sortDal.Item, sourceMap map[int64]recallsource.Source, profile *ranker.UserProfile) map[int64]lrranker.Result {
	if !lrManager.Available() || len(ranked) == 0 {
		return nil
	}
	start := time.Now()
	out := make(map[int64]lrranker.Result, len(ranked))
	for _, ri := range ranked {
		item, ok := itemMap[ri.ItemID]
		if !ok {
			continue
		}
		res, ok := lrManager.Score(buildLRInput(start, profile, item, ri.Scores, sourceMap[ri.ItemID]))
		if !ok {
			return nil
		}
		out[ri.ItemID] = res
	}
	if len(out) > 0 {
		metrics.LRRankerScoredItemsTotal.Add(float64(len(out)))
		metrics.LRRankerScoreDuration.Observe(time.Since(start).Seconds())
	}
	return out
}

// buildLRInput assembles the LR scoring input for one candidate purely from
// objects already in memory (profile, item, baseline breakdown, recall sources,
// request time) — no extra I/O. It mirrors the item_features JSON that the
// training pipeline consumes, so online scoring matches offline training.
func buildLRInput(now time.Time, profile *ranker.UserProfile, item sortDal.Item, scores ranker.ScoreBreakdown, src recallsource.Source) lrranker.Input {
	sem, kw, fr, tot := scores.Semantic, scores.Keyword, scores.Freshness, scores.Total
	quality := item.QualityScore
	createdMS := item.CreatedAt.UnixMilli()

	in := lrranker.Input{
		ServedAtMS:        now.UnixMilli(),
		AgentKeywords:     profile.Keywords,
		AgentDomains:      profile.Domains,
		AgentGeo:          profile.Geo,
		ItemKeywords:      item.Keywords,
		ItemDomains:       item.Domains,
		ItemGeo:           item.Geo,
		BroadcastType:     item.Type,
		SourceType:        item.SourceType,
		Timeliness:        item.Timeliness,
		Lang:              item.Lang,
		QualityScore:      &quality,
		CreatedAtMS:       &createdMS,
		RecallSourceNames: recallsource.Names(src),
		RankScores: lrranker.RankScores{
			Semantic:  &sem,
			Keyword:   &kw,
			Freshness: &fr,
			Total:     &tot,
			IsDraft:   scores.IsDraft,
		},
	}
	if item.ExpireTime != nil {
		expireMS := item.ExpireTime.UnixMilli()
		in.ExpireTimeMS = &expireMS
	}
	return in
}
