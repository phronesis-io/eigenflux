package main

import (
	"context"

	"eigenflux_server/kitex_gen/eigenflux/base"
	search "eigenflux_server/kitex_gen/eigenflux/recordsearch"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/recordsearch"
	"eigenflux_server/pkg/searchguard"
	"eigenflux_server/rpc/pm/dal"
	"errors"
)

func (s *PMServiceImpl) SearchMessages(ctx context.Context, req *search.SearchReq) (*search.SearchResp, error) {
	if invalid := recordsearch.Validate(ctx, req, "pending_verify", "open", "closed"); invalid != nil {
		return &search.SearchResp{Items: []*search.Record{}, BaseResp: invalid}, nil
	}
	request := *req
	req = &request
	result := &search.SearchResp{}
	err := s.searchGuard.Load(ctx, db.RDB, searchguard.Messages, req.OwnerAgentId, req, result,
		func(work context.Context) (any, error) { return dal.SearchMessages(work, db.DB, req) },
		func(check context.Context) error {
			return dal.FilterSearchVisibility(check, db.DB, req.OwnerAgentId, "message", result)
		})
	if err != nil {
		logger.Ctx(ctx).Error("SearchMessages failed", "error", err)
		return &search.SearchResp{Items: []*search.Record{}, BaseResp: searchError(err)}, nil
	}
	return result, nil
}

func (s *PMServiceImpl) SearchFriends(ctx context.Context, req *search.SearchReq) (*search.SearchResp, error) {
	if invalid := recordsearch.Validate(ctx, req, "friend"); invalid != nil {
		return &search.SearchResp{Items: []*search.Record{}, BaseResp: invalid}, nil
	}
	request := *req
	req = &request
	result := &search.SearchResp{}
	err := s.searchGuard.Load(ctx, db.RDB, searchguard.Friends, req.OwnerAgentId, req, result,
		func(work context.Context) (any, error) { return dal.SearchFriends(work, db.DB, req) },
		func(check context.Context) error {
			return dal.FilterSearchVisibility(check, db.DB, req.OwnerAgentId, "friend", result)
		})
	if err != nil {
		logger.Ctx(ctx).Error("SearchFriends failed", "error", err)
		return &search.SearchResp{Items: []*search.Record{}, BaseResp: searchError(err)}, nil
	}
	return result, nil
}

func searchError(err error) *base.BaseResp {
	if errors.Is(err, searchguard.ErrLimited) {
		return &base.BaseResp{Code: 429, Msg: "search rate limited"}
	}
	return &base.BaseResp{Code: 503, Msg: "search unavailable"}
}
