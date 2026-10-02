package main

import (
	"context"

	"eigenflux_server/kitex_gen/eigenflux/base"
	search "eigenflux_server/kitex_gen/eigenflux/recordsearch"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/recordsearch"
	"eigenflux_server/rpc/pm/dal"
)

func (s *PMServiceImpl) SearchMessages(ctx context.Context, req *search.SearchReq) (*search.SearchResp, error) {
	if invalid := recordsearch.Validate(ctx, req, "pending_verify", "open", "closed"); invalid != nil {
		return &search.SearchResp{Items: []*search.Record{}, BaseResp: invalid}, nil
	}
	result, err := dal.SearchMessages(ctx, db.DB, req)
	if err != nil {
		logger.Ctx(ctx).Error("SearchMessages failed", "error", err)
		return &search.SearchResp{Items: []*search.Record{}, BaseResp: &base.BaseResp{Code: 500, Msg: "search unavailable"}}, nil
	}
	return result, nil
}

func (s *PMServiceImpl) SearchFriends(ctx context.Context, req *search.SearchReq) (*search.SearchResp, error) {
	if invalid := recordsearch.Validate(ctx, req, "friend"); invalid != nil {
		return &search.SearchResp{Items: []*search.Record{}, BaseResp: invalid}, nil
	}
	result, err := dal.SearchFriends(ctx, db.DB, req)
	if err != nil {
		logger.Ctx(ctx).Error("SearchFriends failed", "error", err)
		return &search.SearchResp{Items: []*search.Record{}, BaseResp: &base.BaseResp{Code: 500, Msg: "search unavailable"}}, nil
	}
	return result, nil
}
