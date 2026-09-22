package main

import (
	"context"
	sortapi "eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/discovery"
	"eigenflux_server/pkg/discoveryrpc"
	"eigenflux_server/pkg/discoveryserve"
	"encoding/json"
)

type sortExecutor struct{}

func (s sortExecutor) Execute(ctx context.Context, owner int64, r discovery.Request, mode discovery.Mode, _ int64) (discovery.Execution, error) {
	var x discovery.Execution
	b, err := json.Marshal(r)
	if err != nil {
		return x, err
	}
	response, err := sortClient.Discovery(ctx, &sortapi.DiscoveryReq{AgentId: owner, Operation: string(mode), Payload: string(b)})
	err = discoveryrpc.DecodeResponse(response, err, &x)
	return x, err
}
func (s *FeedServiceImpl) Discovery(ctx context.Context, r *sortapi.DiscoveryReq) (*sortapi.DiscoveryResp, error) {
	if s.config == nil || !s.config.EnableNeedSearch {
		return discoveryrpc.Response(nil, discovery.Failure(503, "discovery_disabled")), nil
	}
	if r == nil || r.AgentId <= 0 {
		return discoveryrpc.Response(nil, discovery.Failure(401, "unauthorized")), nil
	}
	request, err := discovery.Decode[discovery.Request]([]byte(r.Payload))
	if err != nil {
		return discoveryrpc.Response(nil, err), nil
	}
	service := discoveryserve.Service{Redis: db.RDB, IDs: s.impressionIDGen, Executor: sortExecutor{}, StreamMaxLen: s.config.MqStreamMaxLen, DisableDedup: s.config.ShouldDisableDedup()}
	response, err := service.Serve(ctx, r.AgentId, request, discovery.Mode(r.Operation), r.GetIdempotencyKey())
	return discoveryrpc.Response(response, err), nil
}
