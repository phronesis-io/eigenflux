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

// TestCreateOrder_HasUnpaidOrdersMessage — a buyer with one delivered (unpaid)
// order is blocked from creating another with msg=has_unpaid_orders.
// GetGateStatus reflects unpaid_order_count=1. Item §9.
func TestCreateOrder_HasUnpaidOrdersMessage(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	defer cleanTradeData(t, sellerID, buyerID)

	svcID := publishTestService(t, cli, sellerID, "gate_unpaid")
	defer testutil.TestDB.Exec("DELETE FROM trading_service_stats WHERE service_id = $1", svcID)

	first, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId:   buyerID,
		ServiceId:      svcID,
		BuyerInput:     "first",
		IdempotencyKey: fmt.Sprintf("gate-unpaid-1-%d", time.Now().UnixNano()),
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), first.BaseResp.Code, "first order should succeed: %s", first.BaseResp.Msg)

	delResp, err := cli.DeliverOrder(context.Background(), &trade.DeliverOrderReq{
		OrderId:         first.OrderId,
		SellerAgentId:   sellerID,
		DeliveryPayload: "done",
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), delResp.BaseResp.Code, "deliver failed: %s", delResp.BaseResp.Msg)

	second, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId:   buyerID,
		ServiceId:      svcID,
		BuyerInput:     "second",
		IdempotencyKey: fmt.Sprintf("gate-unpaid-2-%d", time.Now().UnixNano()),
	})
	require.NoError(t, err)
	require.NotEqual(t, int32(0), second.BaseResp.Code)
	require.Equal(t, "has_unpaid_orders", second.BaseResp.Msg)

	gate, err := cli.GetGateStatus(context.Background(), &trade.GetGateStatusReq{
		BuyerAgentId: buyerID,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), gate.BaseResp.Code)
	require.Equal(t, int32(1), gate.UnpaidOrderCount)
	require.True(t, gate.HasPendingRelease)
	require.False(t, gate.CanCreateOrder)
}

// TestCreateOrder_TooManyActiveMessage — a buyer with 3 created (not delivered)
// orders gets msg=too_many_active_orders on the 4th attempt. Item §9.
func TestCreateOrder_TooManyActiveMessage(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	defer cleanTradeData(t, sellerID, buyerID)

	svcID := publishTestService(t, cli, sellerID, "gate_active")
	defer testutil.TestDB.Exec("DELETE FROM trading_service_stats WHERE service_id = $1", svcID)

	for i := 0; i < 3; i++ {
		r, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
			BuyerAgentId:   buyerID,
			ServiceId:      svcID,
			BuyerInput:     fmt.Sprintf("o%d", i),
			IdempotencyKey: fmt.Sprintf("gate-active-%d-%d", i, time.Now().UnixNano()),
		})
		require.NoError(t, err)
		require.Equal(t, int32(0), r.BaseResp.Code, "order %d should succeed: %s", i, r.BaseResp.Msg)
	}

	fourth, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId:   buyerID,
		ServiceId:      svcID,
		BuyerInput:     "fourth",
		IdempotencyKey: fmt.Sprintf("gate-active-4-%d", time.Now().UnixNano()),
	})
	require.NoError(t, err)
	require.NotEqual(t, int32(0), fourth.BaseResp.Code)
	require.Equal(t, "too_many_active_orders", fourth.BaseResp.Msg)
}
