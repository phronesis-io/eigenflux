package trade_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/trade"
	"eigenflux_server/tests/testutil"

	"github.com/stretchr/testify/require"
)

// TestNotify_OnCreate — seller receives a trade_order_received notification
// after the buyer places an order. Item §7 of 2026-06-18 kovaloop-account-gate
// design.
func TestNotify_OnCreate(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	defer cleanTradeData(t, sellerID, buyerID)
	defer cleanTradeNotifyKeys(sellerID, buyerID)

	svcID := publishTestService(t, cli, sellerID, "notify_create")
	defer testutil.TestDB.Exec("DELETE FROM trading_service_stats WHERE service_id = $1", svcID)

	createResp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId:   buyerID,
		ServiceId:      svcID,
		BuyerInput:     "do the thing",
		IdempotencyKey: fmt.Sprintf("notify-create-%d", time.Now().UnixNano()),
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), createResp.BaseResp.Code, "create order failed: %s", createResp.BaseResp.Msg)

	got := pollTradeNotification(t, sellerID, 30*time.Second)
	require.Equal(t, "trade_order_received", got["type"])
	requireOrderIDMatches(t, got, createResp.OrderId)
	require.Equal(t, "do the thing", got["buyer_input"])
}

// TestNotify_OnDeliver — buyer receives a trade_order_delivered notification
// after the seller delivers. Item §8.
func TestNotify_OnDeliver(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	defer cleanTradeData(t, sellerID, buyerID)
	defer cleanTradeNotifyKeys(sellerID, buyerID)

	svcID := publishTestService(t, cli, sellerID, "notify_deliver")
	defer testutil.TestDB.Exec("DELETE FROM trading_service_stats WHERE service_id = $1", svcID)

	createResp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId:   buyerID,
		ServiceId:      svcID,
		BuyerInput:     "x",
		IdempotencyKey: fmt.Sprintf("notify-deliver-%d", time.Now().UnixNano()),
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), createResp.BaseResp.Code)

	bigPayload := strings.Repeat("y", 800)
	delResp, err := cli.DeliverOrder(context.Background(), &trade.DeliverOrderReq{
		OrderId:         createResp.OrderId,
		SellerAgentId:   sellerID,
		DeliveryPayload: bigPayload,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), delResp.BaseResp.Code, "deliver failed: %s", delResp.BaseResp.Msg)

	got := pollTradeNotification(t, buyerID, 30*time.Second)
	require.Equal(t, "trade_order_delivered", got["type"])
	requireOrderIDMatches(t, got, createResp.OrderId)

	preview, _ := got["delivery_payload_preview"].(string)
	require.LessOrEqual(t, len(preview), 500, "preview must be truncated to <=500")
	require.NotEmpty(t, preview)
}

// pollTradeNotification waits up to timeout for at least one entry to appear
// in trade:notify:{agentID} Redis hash, returning the first decoded payload.
func pollTradeNotification(t *testing.T, agentID int64, timeout time.Duration) map[string]any {
	t.Helper()
	rdb := testutil.GetTestRedis()
	ctx := context.Background()
	key := fmt.Sprintf("trade:notify:%d", agentID)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		values, err := rdb.HVals(ctx, key).Result()
		require.NoError(t, err)
		if len(values) > 0 {
			var payload map[string]any
			require.NoError(t, json.Unmarshal([]byte(values[0]), &payload))
			return payload
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("no trade notification arrived in trade:notify:%d within %s", agentID, timeout)
	return nil
}

// requireOrderIDMatches compares the order_id field (decoded as float64) to
// the expected int64. Encoding/json widens integers to float64, so direct
// equality on the int64 fails — compare via int64 cast.
func requireOrderIDMatches(t *testing.T, payload map[string]any, want int64) {
	t.Helper()
	raw, ok := payload["order_id"]
	require.True(t, ok, "payload missing order_id: %v", payload)
	switch v := raw.(type) {
	case float64:
		require.Equal(t, want, int64(v))
	case string:
		require.Equal(t, fmt.Sprintf("%d", want), v)
	default:
		t.Fatalf("unexpected order_id type %T: %v", raw, raw)
	}
}

func cleanTradeNotifyKeys(agentIDs ...int64) {
	rdb := testutil.GetTestRedis()
	ctx := context.Background()
	for _, id := range agentIDs {
		rdb.Del(ctx, fmt.Sprintf("trade:notify:%d", id))
	}
}
