package main

import (
	"context"

	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/profile"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/recordsearch"
	"eigenflux_server/pkg/reqinfo"
	"eigenflux_server/pkg/searchguard"
	"eigenflux_server/rpc/profile/dal"
	"errors"
)

func (s *ProfileServiceImpl) MatchAgentsByName(ctx context.Context, req *profile.MatchAgentsByNameReq) (*profile.MatchAgentsByNameResp, error) {
	response := &profile.MatchAgentsByNameResp{AgentIds: []int64{}, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}
	if req == nil || !recordsearch.ValidQuery(req.Query) {
		response.BaseResp = &base.BaseResp{Code: 400, Msg: "invalid search"}
		return response, nil
	}

	owner := reqinfo.AuthFromContext(ctx).AgentID
	if owner <= 0 {
		response.BaseResp = &base.BaseResp{Code: 403, Msg: "authenticated caller required"}
		return response, nil
	}
	request := *req
	req = &request
	err := s.searchGuard.Load(ctx, db.RDB, searchguard.AgentNames, owner, req, response, func(work context.Context) (any, error) {
		ids, more, err := dal.MatchAgentsByName(work, db.DB, req.Query)
		if err != nil {
			return nil, err
		}
		return &profile.MatchAgentsByNameResp{AgentIds: ids, HasMore: more, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
	}, nil)
	if err != nil {
		logger.Ctx(ctx).Error("MatchAgentsByName failed", "error", err)
		code := int32(503)
		if errors.Is(err, searchguard.ErrLimited) {
			code = 429
		}
		return &profile.MatchAgentsByNameResp{AgentIds: []int64{}, BaseResp: &base.BaseResp{Code: code, Msg: "search unavailable"}}, nil
	}
	return response, nil
}
