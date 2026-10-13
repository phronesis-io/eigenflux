package main

import (
	"context"
	sortapi "eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/rpc/sort/discovery"
	"eigenflux_server/rpc/sort/discovery/transport"
	"eigenflux_server/rpc/sort/legacy"
	"time"
)

type SortServiceESImpl struct {
	*legacy.Service
	discovery *discovery.Service
}

var _ sortapi.SortService = (*SortServiceESImpl)(nil)

func (s *SortServiceESImpl) Discovery(ctx context.Context, req *sortapi.DiscoveryReq) (*sortapi.DiscoveryResp, error) {
	if req == nil || req.AgentId <= 0 {
		return transport.Response(nil, discovery.Failure(401, "unauthorized")), nil
	}
	if s.discovery == nil {
		return transport.Response(nil, discovery.Failure(503, "discovery_disabled")), nil
	}
	value, err := s.discovery.Run(ctx, req.AgentId, discovery.Operation{Name: req.Operation, Payload: req.Payload}, time.Now().UnixMilli())
	return transport.Response(value, err), nil
}
