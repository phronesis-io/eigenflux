package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"eigenflux_server/kitex_gen/eigenflux/base"
	pmrpc "eigenflux_server/kitex_gen/eigenflux/pm"

	"github.com/cloudwego/hertz/pkg/app"
)

func TestWritePMRateLimitReturnsStructuredHTTP429(t *testing.T) {
	errorCode := "PM_WAITING_FOR_PEER_REPLY"
	limit := int32(3)
	used := int32(3)
	retryAfter := int64(86399)
	resetCondition := "peer_reply_or_timeout"
	ctx := app.NewContext(0)

	writePMRateLimit(ctx, &pmrpc.SendPMResp{
		ConvId:            456,
		ErrorCode:         &errorCode,
		RateLimit:         &limit,
		RateUsed:          &used,
		RetryAfterSeconds: &retryAfter,
		ResetCondition:    &resetCondition,
		BaseResp:          &base.BaseResp{Code: 429, Msg: "waiting for reply"},
	})

	if ctx.Response.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status=%d", ctx.Response.StatusCode())
	}
	if got := string(ctx.Response.Header.Peek("Retry-After")); got != "86399" {
		t.Fatalf("Retry-After=%q", got)
	}
	var payload struct {
		Code  int32  `json:"code"`
		Msg   string `json:"msg"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				Scope             string `json:"scope"`
				ConvID            string `json:"conv_id"`
				Limit             int32  `json:"limit"`
				Used              int32  `json:"used"`
				Remaining         int32  `json:"remaining"`
				ResetCondition    string `json:"reset_condition"`
				RetryAfterSeconds int64  `json:"retry_after_seconds"`
				RecommendedAction string `json:"recommended_action"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(ctx.Response.Body(), &payload); err != nil {
		t.Fatalf("decode response: %v body=%s", err, ctx.Response.Body())
	}
	if payload.Code != 429 || payload.Error.Code != errorCode || payload.Error.Details.ConvID != "456" {
		t.Fatalf("unexpected response: %#v", payload)
	}
	if payload.Error.Details.Limit != 3 || payload.Error.Details.Used != 3 || payload.Error.Details.Remaining != 0 {
		t.Fatalf("unexpected quota details: %#v", payload.Error.Details)
	}
	if payload.Error.Details.Scope != "conversation" || payload.Error.Details.ResetCondition != resetCondition || payload.Error.Details.RetryAfterSeconds != retryAfter {
		t.Fatalf("unexpected retry details: %#v", payload.Error.Details)
	}
	if payload.Msg == "" || payload.Error.Message == "" || payload.Error.Details.RecommendedAction == "" {
		t.Fatalf("agent guidance is incomplete: %#v", payload)
	}
}
