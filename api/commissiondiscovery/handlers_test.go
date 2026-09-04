package commissiondiscovery

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/kitex/client/callopt"

	"eigenflux_server/api/commissionaccess"
	base "eigenflux_server/kitex_gen/eigenflux/base"
	sortmodel "eigenflux_server/kitex_gen/eigenflux/sort"
)

type fakeSort struct {
	searchReq    *sortmodel.SearchCommissionsReq
	recommendReq *sortmodel.RecommendCommissionsReq
}

func (f *fakeSort) SearchCommissions(_ context.Context, req *sortmodel.SearchCommissionsReq, _ ...callopt.Option) (*sortmodel.SearchCommissionsResp, error) {
	f.searchReq = req
	features := "fresh"
	return &sortmodel.SearchCommissionsResp{Candidates: []*sortmodel.CommissionCandidate{{CommissionId: 123, Score: 0.98, Features: &features}}, BaseResp: &base.BaseResp{Code: 0}}, nil
}

func (f *fakeSort) RecommendCommissions(_ context.Context, req *sortmodel.RecommendCommissionsReq, _ ...callopt.Option) (*sortmodel.RecommendCommissionsResp, error) {
	f.recommendReq = req
	return &sortmodel.RecommendCommissionsResp{Candidates: []*sortmodel.CommissionCandidate{{CommissionId: 456, Score: 0.77}}, BaseResp: &base.BaseResp{Code: 0}}, nil
}

type fakeIDGen struct{ next int64 }

func (f *fakeIDGen) NextID() (int64, error) { f.next++; return f.next, nil }

func perform(t *testing.T, h *server.Hertz, method, target string) map[string]any {
	t.Helper()
	recorder := ut.PerformRequest(h.Engine, method, target, nil)
	var payload map[string]any
	if err := json.Unmarshal(recorder.Result().Body(), &payload); err != nil {
		t.Fatalf("status=%d body=%s: %v", recorder.Result().StatusCode(), recorder.Result().Body(), err)
	}
	return payload
}

func TestSearchValidatesAndForwardsFilters(t *testing.T) {
	sortClient := &fakeSort{}
	var mu sync.Mutex
	var event map[string]interface{}
	published := make(chan struct{})
	service := New(sortClient, &fakeIDGen{next: 100}, func(_ context.Context, _ string, values map[string]interface{}) (string, error) {
		mu.Lock()
		event = values
		mu.Unlock()
		close(published)
		return "1-0", nil
	})
	h := server.New()
	// AuthMiddleware is intentionally bypassed here; handler tests exercise the
	// same context value that middleware installs after successful validation.
	h.GET("/test", func(ctx context.Context, c *app.RequestContext) {
		c.Set("agent_id", int64(42))
		service.Search(ctx, c)
	})
	payload := perform(t, h, "GET", "/test?query=go%20work&limit=7&min_price_fen=10&max_price_fen=99&min_promised_delivery_ms=1&max_promised_delivery_ms=500")
	if payload["code"] != float64(0) {
		t.Fatalf("payload=%v", payload)
	}
	if sortClient.searchReq.Query != "go work" || sortClient.searchReq.GetLimit() != 7 {
		t.Fatalf("request=%+v", sortClient.searchReq)
	}
	if sortClient.searchReq.Filters == nil || sortClient.searchReq.Filters.GetMinPriceFen() != 10 || sortClient.searchReq.Filters.GetMaxPriceFen() != 99 {
		t.Fatalf("filters=%+v", sortClient.searchReq.Filters)
	}
	data := payload["data"].(map[string]any)
	if data["impression_id"] != "101" || data["candidates"].([]any)[0].(map[string]any)["commission_id"] != "123" {
		t.Fatalf("data=%v", data)
	}
	<-published
	mu.Lock()
	defer mu.Unlock()
	if event["agent_id"] != "42" || event["operation"] != "search" || !strings.Contains(event["candidates"].(string), "123") {
		t.Fatalf("event=%v", event)
	}
}

func TestRecommendUsesAuthenticatedAgentID(t *testing.T) {
	sortClient := &fakeSort{}
	service := New(sortClient, &fakeIDGen{next: 200}, nil)
	h := server.New()
	h.GET("/test", func(ctx context.Context, c *app.RequestContext) {
		c.Set("agent_id", int64(99))
		service.Recommend(ctx, c)
	})
	payload := perform(t, h, "GET", "/test?limit=3")
	if payload["code"] != float64(0) || sortClient.recommendReq.AgentId != 99 {
		t.Fatalf("payload=%v req=%+v", payload, sortClient.recommendReq)
	}
}

func TestSearchRejectsInvalidRange(t *testing.T) {
	service := New(&fakeSort{}, &fakeIDGen{}, nil)
	h := server.New()
	h.GET("/test", func(ctx context.Context, c *app.RequestContext) {
		c.Set("agent_id", int64(1))
		service.Search(ctx, c)
	})
	payload := perform(t, h, "GET", "/test?query=x&min_price_fen=10&max_price_fen=1")
	if payload["code"] != float64(400) {
		t.Fatalf("payload=%v", payload)
	}
}

func TestDomainValidationResponseRemainsClientError(t *testing.T) {
	h := server.New()
	h.GET("/test", func(ctx context.Context, c *app.RequestContext) {
		validateRPCResponse(ctx, c, "recommend", 400, "profile is incomplete")
	})
	payload := perform(t, h, "GET", "/test")
	if payload["code"] != float64(400) || payload["msg"] != "profile is incomplete" {
		t.Fatalf("payload=%v", payload)
	}
}

func TestRegisteredRoutesRejectUnlistedAgentBeforeSort(t *testing.T) {
	access, err := commissionaccess.New(true, "7")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/commissions/search?query=go", "/api/v1/commissions/recommendations"} {
		t.Run(path, func(t *testing.T) {
			sortClient := &fakeSort{}
			h := server.New()
			trustedAuth := func(ctx context.Context, c *app.RequestContext) {
				c.Set("agent_id", int64(8))
				c.Next(ctx)
			}
			registerRoutes(h, New(sortClient, &fakeIDGen{}, nil), trustedAuth, access.V1Middleware())
			recorder := ut.PerformRequest(h.Engine, http.MethodGet, path, nil)
			if recorder.Result().StatusCode() != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", recorder.Result().StatusCode(), recorder.Result().Body())
			}
			if sortClient.searchReq != nil || sortClient.recommendReq != nil {
				t.Fatalf("Sort called: search=%v recommend=%v", sortClient.searchReq, sortClient.recommendReq)
			}
		})
	}
}
