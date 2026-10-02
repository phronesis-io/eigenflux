package main

import (
	"context"

	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/profile"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/recordsearch"
	"eigenflux_server/rpc/profile/dal"
)

func (s *ProfileServiceImpl) MatchAgentsByName(ctx context.Context, req *profile.MatchAgentsByNameReq) (*profile.MatchAgentsByNameResp, error) {
	response := &profile.MatchAgentsByNameResp{AgentIds: []int64{}, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}
	if req == nil || !recordsearch.ValidQuery(req.Query) {
		response.BaseResp = &base.BaseResp{Code: 400, Msg: "invalid search"}
		return response, nil
	}
	ids, more, err := dal.MatchAgentsByName(ctx, db.DB, req.Query)
	if err != nil {
		logger.Ctx(ctx).Error("MatchAgentsByName failed", "error", err)
		response.BaseResp = &base.BaseResp{Code: 500, Msg: "search unavailable"}
		return response, nil
	}
	response.AgentIds, response.HasMore = ids, more
	return response, nil
}
