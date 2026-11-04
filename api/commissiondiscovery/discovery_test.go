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

type discoveryStub struct {
	last  *sortapi.DiscoveryReq
	items []discovery.ResultItem
}

func (f *discoveryStub) Discovery(_ context.Context, r *sortapi.DiscoveryReq, _ ...callopt.Option) (*sortapi.DiscoveryResp, error) {
	f.last = r
	if f.items != nil {
		return transport.Response(discovery.Response{ImpressionID: "new-42", Items: f.items}, nil), nil
	}
	return transport.Response(discovery.Response{ImpressionID: "new-42", Items: []discovery.ResultItem{{Ref: discovery.SourceRef{Type: discovery.Commission, ID: 42}, Match: map[string]any{"score": .8, "scorer_version": "commission_rules_v1"}}}}, nil), nil
}

func TestCommissionSummarySurvivesPublicItemTransportAndCompatibilityHTTP(t *testing.T) {
	price, duration := int64(0), int64(86400000)
	item := discovery.PublicItem(discovery.Candidate{Document: discovery.Document{Ref: discovery.SourceRef{Type: discovery.Commission, ID: 9007199254740993}, Preview: "Free code review", PriceFen: &price, Currency: "CNY", DurationMS: &duration}, Score: discovery.Score{Value: .8, Version: "test"}})
	for _, mode := range []string{"search", "recommend"} {
		t.Run(mode, func(t *testing.T) {
			old, next := &fakeSort{}, &discoveryStub{items: []discovery.ResultItem{item}}
			s := New(old, &fakeIDGen{}, nil, nil)
			s.SetDiscoveryClient(next)
			h := server.New()
			h.GET("/search", func(ctx context.Context, c *app.RequestContext) { c.Set("agent_id", int64(1)); s.Search(ctx, c) })
			h.GET("/recommend", func(ctx context.Context, c *app.RequestContext) { c.Set("agent_id", int64(1)); s.Recommend(ctx, c) })
			path := "/recommend?limit=7"
			if mode == "search" {
				path = "/search?query=review&limit=7"
			}
			status, out := performWithStatus(t, h, "GET", path)
			if status != 200 {
				t.Fatal(status, out)
			}
			candidate := out["data"].(map[string]any)["candidates"].([]any)[0].(map[string]any)
			if candidate["commission_id"] != "9007199254740993" || candidate["title"] != "Free code review" || candidate["price_fen"] != float64(0) || candidate["currency"] != "CNY" || candidate["promised_delivery_ms"] != float64(duration) || candidate["score"] != .8 || old.searchCalls != 0 || old.recommendCalls != 0 {
				t.Fatalf("summary lost or old lookup called: %v", candidate)
			}
		})
	}
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
	status, out = performWithStatus(t, h, "GET", "/search?commission_id=9223372036854775807&min_price_fen=10")
	if status != 200 || old.searchCalls != 0 || next.last == nil {
		t.Fatal("exact lookup did not migrate", status, out)
	}
	in = discovery.Request{}
	if err := json.Unmarshal([]byte(next.last.Payload), &in); err != nil {
		t.Fatal(err)
	}
	if in.Query != "" || in.CommissionID != 9223372036854775807 || in.Filters.Currency != "CNY" || in.Filters.MinPriceFen == nil || *in.Filters.MinPriceFen != 10 {
		t.Fatal("exact request changed", in)
	}
	data = out["data"].(map[string]any)
	if data["impression_id"] != "new-42" || data["candidates"].([]any)[0].(map[string]any)["commission_id"] != "42" {
		t.Fatal("compatibility response changed", data)
	}
}

func TestCommissionRecommendationForwardsRequestedLimit(t *testing.T) {
	next := &discoveryStub{}
	s := New(&fakeSort{}, &fakeIDGen{}, nil, nil)
	s.SetDiscoveryClient(next)
	h := server.New()
	h.GET("/recommend", func(ctx context.Context, c *app.RequestContext) { c.Set("agent_id", int64(1)); s.Recommend(ctx, c) })
	status, body := performWithStatus(t, h, "GET", "/recommend?limit=7")
	if status != 200 || next.last == nil {
		t.Fatal(status, body)
	}
	var request discovery.Request
	if err := json.Unmarshal([]byte(next.last.Payload), &request); err != nil {
		t.Fatal(err)
	}
	if request.Limit != 7 || next.last.Operation != "recommendation" {
		t.Fatal(request, next.last)
	}
}
