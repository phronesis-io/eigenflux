package commissiondiscovery

import (
	"context"
	"encoding/json"
	"testing"

	sortapi "eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/rpc/sort/discovery"
	"eigenflux_server/rpc/sort/discovery/transport"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/kitex/client/callopt"
)

type discoveryStub struct{ last *sortapi.DiscoveryReq }

func (f *discoveryStub) Discovery(_ context.Context, r *sortapi.DiscoveryReq, _ ...callopt.Option) (*sortapi.DiscoveryResp, error) {
	f.last = r
	return transport.Response(discovery.Response{ImpressionID: "new-42", Items: []discovery.ResultItem{{Ref: discovery.SourceRef{Type: discovery.Commission, ID: 42}, Match: map[string]any{"score": .8, "scorer_version": "commission_rules_v1"}}}}, nil), nil
}
func TestLegacyCommissionCutoverPreservesExactLookupAndCurrency(t *testing.T) {
	old := &fakeSort{}
	next := &discoveryStub{}
	s := New(old, &fakeIDGen{}, nil, nil)
	s.SetDiscoveryClient(next)
	h := server.New()
	h.GET("/search", func(ctx context.Context, c *app.RequestContext) { c.Set("agent_id", int64(1)); s.Search(ctx, c) })
	status, out := performWithStatus(t, h, "GET", "/search?query=design&min_price_fen=0&limit=100")
	if status != 200 || next.last == nil || old.searchCalls != 0 {
		t.Fatal(status, out, next.last)
	}
	var in discovery.Request
	if err := json.Unmarshal([]byte(next.last.Payload), &in); err != nil {
		t.Fatal(err)
	}
	if next.last.Operation != "legacy_search" || in.Limit != 100 || in.Filters.Currency != "CNY" || in.Filters.MinPriceFen == nil || *in.Filters.MinPriceFen != 0 {
		t.Fatal(next.last, in)
	}
	data := out["data"].(map[string]any)
	if data["impression_id"] != "new-42" {
		t.Fatal(data)
	}
	next.last = nil
	status, _ = performWithStatus(t, h, "GET", "/search?commission_id=42")
	if status != 200 || old.searchCalls != 1 || next.last != nil {
		t.Fatal("exact lookup changed")
	}
}
