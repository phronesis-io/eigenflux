package trade_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/trade"
	"eigenflux_server/tests/testutil"

	"github.com/stretchr/testify/require"
)

func TestDeliverOrder_RetryIsIdempotent(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	defer cleanTradeData(t, sellerID, buyerID)

	svcID := publishTestService(t, cli, sellerID, "retry-deliver")

	createResp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId:   buyerID,
		ServiceId:      svcID,
		BuyerInput:     "x",
		IdempotencyKey: fmt.Sprintf("retry-%d", time.Now().UnixNano()),
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), createResp.BaseResp.Code)
	orderID := createResp.OrderId

	delResp, err := cli.DeliverOrder(context.Background(), &trade.DeliverOrderReq{
		OrderId:         orderID,
		SellerAgentId:   sellerID,
		DeliveryPayload: "done",
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), delResp.BaseResp.Code, "msg=%s", delResp.BaseResp.Msg)

	// Retry — must return success (200).
	delResp2, err := cli.DeliverOrder(context.Background(), &trade.DeliverOrderReq{
		OrderId:         orderID,
		SellerAgentId:   sellerID,
		DeliveryPayload: "done",
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), delResp2.BaseResp.Code, "retry must return success, got msg=%s", delResp2.BaseResp.Msg)

	var eventCount int64
	require.NoError(t, testutil.TestDB.QueryRow(
		"SELECT COUNT(*) FROM trade_order_events WHERE order_id = $1 AND event_type = $2",
		orderID, int16(3), // EventTypeDelivered
	).Scan(&eventCount))
	require.Equal(t, int64(1), eventCount, "exactly one delivered event row")
}

