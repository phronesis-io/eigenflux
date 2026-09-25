package main

import (
	"context"
	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/feed"
	"eigenflux_server/kitex_gen/eigenflux/item"
	"eigenflux_server/pkg/db"
	"eigenflux_server/rpc/feed/delivery"
	"eigenflux_server/rpc/sort/discovery"
	"encoding/json"
)

// Legacy pages retain one frozen impression and absolute sample positions.
func (s *FeedServiceImpl) fetchDiscoveryFeed(ctx context.Context, owner int64, action string, limit int) (*feed.FetchFeedResp, error) {
	out := &feed.FetchFeedResp{Items: []*feed.FeedItem{}, HasMore: false, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}
	prepare := func(ctx context.Context, x *discovery.Execution) error {
		if len(x.Candidates) == 0 {
			return nil
		}
		ids := []int64{}
		for _, c := range x.Candidates {
			ids = append(ids, c.Document.Ref.ID)
		}
		r, err := itemClient.BatchGetItems(ctx, &item.BatchGetItemsReq{ItemIds: ids})
		if err != nil {
			return err
		}
		if r == nil || r.BaseResp == nil || r.BaseResp.Code != 0 {
			return discovery.Failure(503, "item_hydration_failed")
		}
		m := map[int64]*item.ProcessedItem{}
		for _, p := range r.Items {
			m[p.ItemId] = p
		}
		// BatchGetItems returns only completed content. Missing candidates are
		// skipped while assembling this page, without another Sort round trip.
		candidates := x.Candidates[:0]
		ids = ids[:0]
		for _, c := range x.Candidates {
			if _, ok := m[c.Document.Ref.ID]; ok {
				candidates = append(candidates, c)
				ids = append(ids, c.Document.Ref.ID)
			}
		}
		x.Candidates = candidates
		items := s.buildFeedItems(ctx, owner, ids, m)
		if len(items) != len(ids) {
			return discovery.Failure(503, "item_hydration_failed")
		}
		out.Items = append(out.Items, items...)
		return nil
	}
	service := delivery.Service{Redis: db.RDB, IDs: s.impressionIDGen, Executor: sortExecutor{}, StreamMaxLen: s.config.MqStreamMaxLen, DisableDedup: s.config.ShouldDisableDedup()}
	r, hasMore, err := service.ServePage(ctx, owner, action, limit, prepare)
	if err != nil {
		out.Items = []*feed.FeedItem{}
		out.BaseResp = &base.BaseResp{Code: 503, Msg: "discovery unavailable"}
		if e, ok := err.(*discovery.Error); ok {
			out.BaseResp = &base.BaseResp{Code: int32(e.Code), Msg: e.Reason}
		}
		return out, nil
	}
	out.HasMore = hasMore
	out.ImpressionId = r.ImpressionID
	b, err := json.Marshal(discovery.PublicResponse(r))
	if err != nil {
		return nil, err
	}
	metadata := string(b)
	out.DiscoveryMetadata = &metadata
	return out, nil
}
