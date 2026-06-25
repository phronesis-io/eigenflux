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

// TestDeliveredNeverExpires — a delivered order whose deadline_at has passed
// must NOT be transitioned to expired by the cron scanner. Item §10 of
// 2026-06-18 kovaloop-account-gate design: payment is always required once
// delivered.
func TestDeliveredNeverExpires(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	defer cleanTradeData(t, sellerID, buyerID)

	svcID := publishTestService(t, cli, sellerID, "delivered_no_expiry")
	defer testutil.TestDB.Exec("DELETE FROM trading_service_stats WHERE service_id = $1", svcID)

	createResp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId:   buyerID,
		ServiceId:      svcID,
		BuyerInput:     "x",
		IdempotencyKey: fmt.Sprintf("no-expiry-%d", time.Now().UnixNano()),
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), createResp.BaseResp.Code)

	delResp, err := cli.DeliverOrder(context.Background(), &trade.DeliverOrderReq{
		OrderId:         createResp.OrderId,
		SellerAgentId:   sellerID,
		DeliveryPayload: "done",
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), delResp.BaseResp.Code)

	_, err = testutil.TestDB.Exec(
		"UPDATE trade_orders SET deadline_at = 1 WHERE order_id = $1", createResp.OrderId,
	)
	require.NoError(t, err)

	// TRADE_EXPIRY_SCAN_INTERVAL_SEC defaults to 30s. Wait ~45s and assert
	// status stayed delivered.
	time.Sleep(45 * time.Second)

	var status int16
	require.NoError(t, testutil.TestDB.QueryRow(
		"SELECT status FROM trade_orders WHERE order_id = $1", createResp.OrderId,
	).Scan(&status))
	require.Equal(t, int16(2), status, "delivered order must not move to expired") // OrderStatusDelivered

	var expiredEvents int64
	require.NoError(t, testutil.TestDB.QueryRow(
		"SELECT COUNT(*) FROM trade_order_events WHERE order_id = $1 AND event_type = $2",
		createResp.OrderId, int16(6), // EventTypeExpired
	).Scan(&expiredEvents))
	require.Equal(t, int64(0), expiredEvents, "no expired event should be written for delivered order")
}
