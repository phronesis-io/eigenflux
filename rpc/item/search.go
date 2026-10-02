package main

import (
	"context"

	"eigenflux_server/kitex_gen/eigenflux/base"
	search "eigenflux_server/kitex_gen/eigenflux/recordsearch"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/recordsearch"
	"eigenflux_server/rpc/item/dal"
)

func (s *ItemServiceImpl) SearchOwnedBroadcasts(ctx context.Context, req *search.SearchReq) (*search.SearchResp, error) {
	if invalid := recordsearch.Validate(ctx, req, "pending", "processing", "failed", "published", "discarded", "retracted"); invalid != nil {
		return &search.SearchResp{Items: []*search.Record{}, BaseResp: invalid}, nil
	}
	result, err := dal.SearchOwnedBroadcasts(ctx, db.DB, req)
	if err != nil {
		logger.Ctx(ctx).Error("SearchOwnedBroadcasts failed", "error", err)
		return &search.SearchResp{Items: []*search.Record{}, BaseResp: &base.BaseResp{Code: 500, Msg: "search unavailable"}}, nil
	}
	return result, nil
}
