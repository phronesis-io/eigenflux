package main

import (
	"context"
	sortapi "eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/pkg/db"
	"eigenflux_server/rpc/feed/delivery"
	"eigenflux_server/rpc/sort/discovery"
	"eigenflux_server/rpc/sort/discovery/transport"
	"encoding/json"

	"gorm.io/gorm"
)

type sortExecutor struct{}

func (s sortExecutor) Execute(ctx context.Context, owner int64, r discovery.Request, mode discovery.Mode, _ int64) (discovery.Execution, error) {
	var x discovery.Execution
	b, err := json.Marshal(r)
	if err != nil {
		return x, err
	}
	response, err := sortClient.Discovery(ctx, &sortapi.DiscoveryReq{AgentId: owner, Operation: string(mode), Payload: string(b)})
	err = transport.DecodeResponse(response, err, &x)
	return x, err
}
func (s *FeedServiceImpl) Discovery(ctx context.Context, r *sortapi.DiscoveryReq) (*sortapi.DiscoveryResp, error) {
	if s.config == nil || !s.config.EnableNeedSearch {
		return transport.Response(nil, discovery.Failure(503, "discovery_disabled")), nil
	}
	if r == nil || r.AgentId <= 0 {
		return transport.Response(nil, discovery.Failure(401, "unauthorized")), nil
	}
	request, err := discovery.Decode[discovery.Request]([]byte(r.Payload))
	if err != nil {
		return transport.Response(nil, err), nil
	}
	service := delivery.Service{Redis: db.RDB, IDs: s.impressionIDGen, Executor: sortExecutor{}, StreamMaxLen: s.config.MqStreamMaxLen, DisableDedup: s.config.ShouldDisableDedup()}
	response, err := service.Serve(ctx, r.AgentId, request, discovery.Mode(r.Operation), r.GetIdempotencyKey())
	if err == nil {
		err = filterActiveDiscoveryAgents(ctx, db.DB, &response)
	}
	return transport.Response(response, err), nil
}

// Frozen pages and idempotency replays bypass Sort hydration. Keep their
// ordering and cursor boundaries while enforcing current public visibility.
func filterActiveDiscoveryAgents(ctx context.Context, database *gorm.DB, response *discovery.Response) error {
	var ids []int64
	for _, item := range response.Items {
		if item.Ref.Type == discovery.Agent {
			ids = append(ids, item.Ref.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var active []int64
	if err := database.WithContext(ctx).Table("agents").Where("agent_id IN ? AND identity_state='active'", ids).Pluck("agent_id", &active).Error; err != nil {
		return err
	}
	visible := make(map[int64]bool, len(active))
	for _, id := range active {
		visible[id] = true
	}
	kept := response.Items[:0]
	for _, item := range response.Items {
		if item.Ref.Type != discovery.Agent || visible[item.Ref.ID] {
			kept = append(kept, item)
		}
	}
	response.Items = kept
	return nil
}
