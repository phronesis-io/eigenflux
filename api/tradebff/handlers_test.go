package tradebff

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
)

func TestTradeOverviewDoesNotInventUnavailableMoney(t *testing.T) {
	ctx := app.NewContext(0)
	ctx.Request.SetRequestURI("/api/v2/console/bff/trade/overview")
	ctx.Set("agent_id", int64(42))
	New().TradeOverview(context.Background(), ctx)
	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("status = %d", ctx.Response.StatusCode())
	}
	var envelope struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(ctx.Response.Body(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data["available"] != false || envelope.Data["withdrawable_fen"] != nil {
		t.Fatalf("unexpected response: %#v", envelope.Data)
	}
	if envelope.Data["todo_code"] != "COMMISSION_IDENTITY_DELEGATION_REQUIRED" {
		t.Fatalf("todo_code = %#v", envelope.Data["todo_code"])
	}
}
