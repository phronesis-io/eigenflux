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

func TestExpiry_EmitsEventAndStats(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	defer cleanTradeData(t, sellerID, buyerID)

	svcID := publishTestService(t, cli, sellerID, "expiry")
	defer testutil.TestDB.Exec("DELETE FROM trading_service_stats WHERE service_id = $1", svcID)

	createResp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId:   buyerID,
		ServiceId:      svcID,
		BuyerInput:     "x",
		IdempotencyKey: fmt.Sprintf("expiry-%d", time.Now().UnixNano()),
	})
	require.NoError(t, err)
	orderID := createResp.OrderId

	// Force deadline_at into the past so the scanner picks it up next tick.
	_, err = testutil.TestDB.Exec(
		"UPDATE trade_orders SET deadline_at = 1 WHERE order_id = $1", orderID,
	)
	require.NoError(t, err)

	// Default TRADE_EXPIRY_SCAN_INTERVAL_SEC = 30. Outbox dispatch interval is 1s.
	// Wait up to 45s for the full chain to complete.
	deadline := time.Now().Add(45 * time.Second)
	var expiredCount int32
	for time.Now().Before(deadline) {
		_ = testutil.TestDB.QueryRow(
			"SELECT expired_count FROM trading_service_stats WHERE service_id = $1", svcID,
		).Scan(&expiredCount)
		if expiredCount >= 1 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	require.Equal(t, int32(1), expiredCount, "expired_count must increment within scan + dispatch window")

	var eventCount int64
	require.NoError(t, testutil.TestDB.QueryRow(
		"SELECT COUNT(*) FROM trade_order_events WHERE order_id = $1 AND event_type = $2",
		orderID, int16(6), // EventTypeExpired
	).Scan(&eventCount))
	require.Equal(t, int64(1), eventCount, "exactly one expired event row")
}
