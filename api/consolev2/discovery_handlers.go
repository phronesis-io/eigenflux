package consolev2

import (
	"context"
	"eigenflux_server/api/commissionaccess"
	sortapi "eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/kitex_gen/eigenflux/sort/sortservice"
	"eigenflux_server/rpc/sort/discovery"
	"eigenflux_server/rpc/sort/discovery/transport"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

func (s *Service) RegisterDiscovery(h *server.Hertz, sortClient sortservice.Client, access *commissionaccess.Allowlist) {
	handler := func(operation string) app.HandlerFunc {
		return func(ctx context.Context, c *app.RequestContext) {
			owner, ok := agentID(c)
			if !ok {
				c.JSON(401, map[string]any{"code": 401, "msg": "unauthorized", "data": map[string]any{}})
				return
			}
			raw := append([]byte(nil), c.Request.Body()...)
			if len(raw) > 64<<10 {
				c.JSON(413, map[string]any{"code": 413, "msg": "request_too_large", "data": map[string]any{}})
				return
			}
			if len(raw) == 0 {
				raw = []byte("{}")
			}
			if operation == "taxonomy" {
				p := map[string]any{"query": c.Query("query"), "category": c.Query("category"), "subtype": c.Query("subtype")}
				if v := c.Query("limit"); v != "" {
					n, e := strconv.Atoi(v)
					if e != nil {
						discoveryHTTP(c, transport.Response(nil, discovery.Invalid("limit", "invalid")))
						return
					}
					p["limit"] = n
				}
				raw, _ = json.Marshal(p)
			}
			serving := operation == "search" || operation == "recommendation"
			if serving {
				r, err := discovery.Decode[discovery.Request](raw)
				if err != nil {
					discoveryHTTP(c, transport.Response(nil, err))
					return
				}
				commissionScope := len(r.SourceKinds) == 0
				for _, k := range r.SourceKinds {
					commissionScope = commissionScope || k == discovery.Commission
				}
				if r.Need != nil {
					commissionScope = discovery.Kind(r.Need.NeedType) == discovery.Commission
				}
				if commissionScope && !access.Allows(owner) {
					discoveryHTTP(c, transport.Response(nil, discovery.Failure(403, "commission_access_denied")))
					return
				}
			}
			key := string(c.GetHeader("Idempotency-Key"))
			req := &sortapi.DiscoveryReq{AgentId: owner, Operation: operation, Payload: string(raw), IdempotencyKey: &key}
			var resp *sortapi.DiscoveryResp
			var err error
			if serving {
				resp, err = s.feedClient.Discovery(ctx, req)
			} else {
				resp, err = sortClient.Discovery(ctx, req)
			}
			if err != nil || resp == nil {
				discoveryHTTP(c, transport.Response(nil, discovery.Failure(503, "discovery_unavailable")))
				return
			}
			if serving && resp.BaseResp != nil && resp.BaseResp.Code == 0 {
				var result discovery.Response
				if err := json.Unmarshal([]byte(resp.Payload), &result); err != nil {
					discoveryHTTP(c, transport.Response(nil, discovery.Failure(503, "invalid_discovery_response")))
					return
				}
				resp = transport.Response(discovery.PublicResponse(result), nil)
			}
			discoveryHTTP(c, resp)
		}
	}
	for _, r := range []struct{ method, path, op, scope string }{{"POST", "/api/v2/discovery/search", "search", "feed:read"}, {"POST", "/api/v2/discovery/recommendations", "recommendation", "feed:read"}, {"GET", "/api/v2/taxonomy/search", "taxonomy", "feed:read"}} {
		h.Handle(r.method, r.path, s.agentAuth(r.scope), s.requireCompleted, handler(r.op))
	}
}
func discoveryHTTP(c *app.RequestContext, r *sortapi.DiscoveryResp) {
	status := http.StatusOK
	if r.BaseResp.Code != 0 {
		status = int(r.BaseResp.Code)
		if status < 400 || status > 599 {
			status = 503
		}
	}
	c.JSON(status, map[string]any{"code": r.BaseResp.Code, "msg": r.BaseResp.Msg, "data": json.RawMessage(r.Payload)})
}
