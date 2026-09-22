package commissiondiscovery

import (
	"context"
	sortapi "eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/rpc/sort/discovery"
	"eigenflux_server/rpc/sort/discovery/transport"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/kitex/client/callopt"
)

type DiscoveryClient interface {
	Discovery(context.Context, *sortapi.DiscoveryReq, ...callopt.Option) (*sortapi.DiscoveryResp, error)
}

func (s *Service) SetDiscoveryClient(c DiscoveryClient) { s.discoveryClient = c }
func (s *Service) serveDiscovery(ctx context.Context, c *app.RequestContext, mode, query string, f filters, limit int32) {
	owner, ok := callerAgentID(c)
	if !ok {
		return
	}
	r := discovery.Request{Query: query, SourceKinds: []discovery.Kind{discovery.Commission}, Limit: int(limit), Filters: discovery.Filters{Currency: "CNY", MinPriceFen: f.MinPriceFen, MaxPriceFen: f.MaxPriceFen, MinDurationMS: f.MinPromisedDeliveryMS, MaxDurationMS: f.MaxPromisedDeliveryMS}}
	raw, err := json.Marshal(r)
	if err != nil {
		rpcError(ctx, c, mode, err)
		return
	}
	key := string(c.GetHeader("Idempotency-Key"))
	resp, err := s.discoveryClient.Discovery(ctx, &sortapi.DiscoveryReq{AgentId: owner, Operation: mode, Payload: string(raw), IdempotencyKey: &key})
	var out discovery.Response
	if err = transport.DecodeResponse(resp, err, &out); err != nil {
		if e, ok := err.(*discovery.Error); ok {
			respond(c, e.Code, e.Code, e.Reason, map[string]any{})
			return
		}
		rpcError(ctx, c, mode, err)
		return
	}
	candidates := []candidateDTO{}
	for _, it := range out.Items {
		if it.Ref.Type != discovery.Commission {
			rpcError(ctx, c, mode, fmt.Errorf("unexpected source kind"))
			return
		}
		features, _ := json.Marshal(it.Match)
		fs := string(features)
		score, _ := it.Match["score"].(float64)
		candidates = append(candidates, candidateDTO{CommissionID: fmt.Sprint(it.Ref.ID), Score: score, Features: &fs})
	}
	respond(c, 200, 0, "success", map[string]any{"impression_id": out.ImpressionID, "candidates": candidates})
	s.publishAttribution(ctx, mode, out.ImpressionID, owner, query, f, candidates)
}
