package main

import (
	"context"
	"strings"
	"testing"

	handler "console.eigenflux.ai/handler_gen/eigenflux/servicereview"
	api "console.eigenflux.ai/kitex_gen/eigenflux/commission"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/kitex/client/callopt"
)

type reviewRouteClient struct{ resolved bool }

func (*reviewRouteClient) ListModerations(context.Context, *api.ListModerationsReq, ...callopt.Option) (*api.ListModerationsResp, error) {
	return &api.ListModerationsResp{BaseResp: &api.ReviewBaseResp{}, ResultJson: `{"items":[],"next_cursor":""}`}, nil
}
func (f *reviewRouteClient) ResolveModeration(context.Context, *api.ResolveModerationReq, ...callopt.Option) (*api.ResolveModerationResp, error) {
	f.resolved = true
	return &api.ResolveModerationResp{BaseResp: &api.ReviewBaseResp{Code: 409, Msg: "internal detail"}}, nil
}
func TestGeneratedServiceReviewRoutes(t *testing.T) {
	client := &reviewRouteClient{}
	handler.InitServiceReviewClient(client)
	h := server.New()
	register(h)
	result := ut.PerformRequest(h.Engine, "GET", "/console/api/v1/service-reviews", nil).Result()
	if result.StatusCode() != 200 || !strings.Contains(string(result.Body()), `"items":[]`) {
		t.Fatalf("generated GET: %d %s", result.StatusCode(), result.Body())
	}
	body := `{"decision":"pass","operator":"test","reason":"seller-visible"}`
	result = ut.PerformRequest(h.Engine, "POST", "/console/api/v1/service-reviews/123/resolve", &ut.Body{Body: strings.NewReader(body), Len: len(body)}, ut.Header{Key: "Content-Type", Value: "application/json"}).Result()
	if !client.resolved || result.StatusCode() != 409 || !strings.Contains(string(result.Body()), `"code":1`) || strings.Contains(string(result.Body()), "internal detail") {
		t.Fatalf("generated POST: %d %s", result.StatusCode(), result.Body())
	}
}
