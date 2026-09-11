package commissiondiscovery

import (
	"context"
	"encoding/json"
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
	searchReq      *sortmodel.SearchCommissionsReq
	recommendReq   *sortmodel.RecommendCommissionsReq
	searchCalls    int
	recommendCalls int
}

func (f *fakeSort) SearchCommissions(_ context.Context, req *sortmodel.SearchCommissionsReq, _ ...callopt.Option) (*sortmodel.SearchCommissionsResp, error) {
	f.searchCalls++
	f.searchReq = req
	features := "fresh"
	return &sortmodel.SearchCommissionsResp{Candidates: []*sortmodel.CommissionCandidate{{CommissionId: 123, Score: 0.98, Features: &features}}, BaseResp: &base.BaseResp{Code: 0}}, nil
}

func (f *fakeSort) RecommendCommissions(_ context.Context, req *sortmodel.RecommendCommissionsReq, _ ...callopt.Option) (*sortmodel.RecommendCommissionsResp, error) {
	f.recommendCalls++
	f.recommendReq = req
	return &sortmodel.RecommendCommissionsResp{Candidates: []*sortmodel.CommissionCandidate{{CommissionId: 456, Score: 0.77}}, BaseResp: &base.BaseResp{Code: 0}}, nil
}

type fakeIDGen struct{ next int64 }

func (f *fakeIDGen) NextID() (int64, error) { f.next++; return f.next, nil }

func perform(t *testing.T, h *server.Hertz, method, target string) map[string]any {
	t.Helper()
	_, payload := performWithStatus(t, h, method, target)
	return payload
}

func performWithStatus(t *testing.T, h *server.Hertz, method, target string) (int, map[string]any) {
	t.Helper()
	recorder := ut.PerformRequest(h.Engine, method, target, nil)
	var payload map[string]any
	if err := json.Unmarshal(recorder.Result().Body(), &payload); err != nil {
		t.Fatalf("status=%d body=%s: %v", recorder.Result().StatusCode(), recorder.Result().Body(), err)
	}
	return recorder.Result().StatusCode(), payload
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
	}, nil)
	h := server.New()
	Register(h, service)
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
	service := New(sortClient, &fakeIDGen{next: 200}, nil, nil)
	h := server.New()
	h.GET("/test", func(ctx context.Context, c *app.RequestContext) {
		c.Set("agent_id", int64(99))
		service.Recommend(ctx, c)
	})
	Register(h, service)
	// Register adds the production path; use the direct wrapper for the test.
	payload := perform(t, h, "GET", "/test?limit=3")
	if payload["code"] != float64(0) || sortClient.recommendReq.AgentId != 99 {
		t.Fatalf("payload=%v req=%+v", payload, sortClient.recommendReq)
	}
}

func TestSearchRejectsInvalidRange(t *testing.T) {
	service := New(&fakeSort{}, &fakeIDGen{}, nil, nil)
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

func TestSearchForwardsExactCommissionID(t *testing.T) {
	sortClient := &fakeSort{}
	service := New(sortClient, &fakeIDGen{next: 100}, nil, nil)
	h := server.New()
	h.GET("/test", func(ctx context.Context, c *app.RequestContext) {
		c.Set("agent_id", int64(1))
		service.Search(ctx, c)
	})

	status, payload := performWithStatus(t, h, "GET", "/test?commission_id=9223372036854775807&min_price_fen=10")
	if status != 200 || payload["code"] != float64(0) {
		t.Fatalf("status=%d payload=%v", status, payload)
	}
	if sortClient.searchCalls != 1 || sortClient.searchReq.Query != "" || !sortClient.searchReq.IsSetCommissionId() || sortClient.searchReq.GetCommissionId() != 9223372036854775807 {
		t.Fatalf("calls=%d request=%+v", sortClient.searchCalls, sortClient.searchReq)
	}
	if sortClient.searchReq.Filters == nil || sortClient.searchReq.Filters.GetMinPriceFen() != 10 {
		t.Fatalf("filters=%+v", sortClient.searchReq.Filters)
	}
}

func TestSearchRejectsInvalidSearchModeBeforeCallingSort(t *testing.T) {
	for _, target := range []string{
		"/test",
		"/test?query=research&commission_id=42",
		"/test?commission_id=invalid",
		"/test?commission_id=0",
		"/test?commission_id=-1",
		"/test?commission_id=9223372036854775808",
	} {
		t.Run(target, func(t *testing.T) {
			sortClient := &fakeSort{}
			service := New(sortClient, &fakeIDGen{}, nil, nil)
			h := server.New()
			h.GET("/test", func(ctx context.Context, c *app.RequestContext) {
				c.Set("agent_id", int64(1))
				service.Search(ctx, c)
			})

			status, payload := performWithStatus(t, h, "GET", target)
			if status != 400 || payload["code"] != float64(400) {
				t.Fatalf("status=%d payload=%v", status, payload)
			}
			if sortClient.searchCalls != 0 {
				t.Fatalf("invalid request called Sort %d times", sortClient.searchCalls)
			}
		})
	}
}

func TestDiscoveryRejectsUnlistedAgentBeforeCallingSort(t *testing.T) {
	access, err := commissionaccess.New(true, "42")
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"search", "recommend"} {
		t.Run(operation, func(t *testing.T) {
			sortClient := &fakeSort{}
			service := New(sortClient, &fakeIDGen{next: 100}, nil, access)
			h := server.New()
			h.GET("/test", func(ctx context.Context, c *app.RequestContext) {
				c.Set("agent_id", int64(41))
				if operation == "search" {
					service.Search(ctx, c)
					return
				}
				service.Recommend(ctx, c)
			})
			target := "/test"
			if operation == "search" {
				target += "?query=research"
			}
			status, payload := performWithStatus(t, h, "GET", target)
			if status != 403 || payload["code"] != float64(403) || payload["msg"] != "commission access is not allowed" {
				t.Fatalf("status=%d payload=%v", status, payload)
			}
			if sortClient.searchCalls != 0 || sortClient.recommendCalls != 0 {
				t.Fatalf("denied request called Sort: search=%d recommend=%d", sortClient.searchCalls, sortClient.recommendCalls)
			}
		})
	}
}

func TestDiscoveryAllowsListedAgent(t *testing.T) {
	access, err := commissionaccess.New(true, "42")
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"search", "recommend"} {
		t.Run(operation, func(t *testing.T) {
			sortClient := &fakeSort{}
			service := New(sortClient, &fakeIDGen{next: 100}, nil, access)
			h := server.New()
			h.GET("/test", func(ctx context.Context, c *app.RequestContext) {
				c.Set("agent_id", int64(42))
				if operation == "search" {
					service.Search(ctx, c)
					return
				}
				service.Recommend(ctx, c)
			})
			target := "/test"
			if operation == "search" {
				target += "?query=research"
			}
			payload := perform(t, h, "GET", target)
			if payload["code"] != float64(0) || sortClient.searchCalls+sortClient.recommendCalls != 1 {
				t.Fatalf("payload=%v searchCalls=%d recommendCalls=%d", payload, sortClient.searchCalls, sortClient.recommendCalls)
			}
		})
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
