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

func TestOutbox_RefundPublishesToStream(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	defer cleanTradeData(t, sellerID, buyerID)

	svcID := publishTestService(t, cli, sellerID, "outbox")
	defer testutil.TestDB.Exec("DELETE FROM trading_service_stats WHERE service_id = $1", svcID)

	createResp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId:   buyerID,
		ServiceId:      svcID,
		BuyerInput:     "x",
		IdempotencyKey: fmt.Sprintf("outbox-%d", time.Now().UnixNano()),
	})
	require.NoError(t, err)
	orderID := createResp.OrderId

	_, _ = cli.DeliverOrder(context.Background(), &trade.DeliverOrderReq{
		OrderId: orderID, SellerAgentId: sellerID, DeliveryPayload: "x",
	})

	refundResp, err := cli.RefundOrder(context.Background(), &trade.RefundOrderReq{
		OrderId: orderID, ActorAgentId: buyerID,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), refundResp.BaseResp.Code)

	// Use JSONB containment operator to match the order_id field regardless of
	// key ordering or whitespace normalization applied by PostgreSQL.
	orderFilter := fmt.Sprintf(`{"order_id": "%d"}`, orderID)

	// Outbox row exists immediately after the refund commits.
	var outboxCount int64
	require.NoError(t, testutil.TestDB.QueryRow(
		"SELECT COUNT(*) FROM trade_outbox WHERE payload_json @> $1::jsonb",
		orderFilter,
	).Scan(&outboxCount))
	require.Equal(t, int64(1), outboxCount, "exactly one outbox row for the refund")

	// Within 3s the dispatcher publishes and marks it.
	deadline := time.Now().Add(3 * time.Second)
	var publishedCount int64
	for time.Now().Before(deadline) {
		require.NoError(t, testutil.TestDB.QueryRow(
			"SELECT COUNT(*) FROM trade_outbox WHERE status = 1 AND payload_json @> $1::jsonb",
			orderFilter,
		).Scan(&publishedCount))
		if publishedCount == 1 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.Equal(t, int64(1), publishedCount, "dispatcher should publish within 3s")

	// And within 5s the consumer should bump refunded_count.
	deadline = time.Now().Add(5 * time.Second)
	var refundedCount int32
	for time.Now().Before(deadline) {
		_ = testutil.TestDB.QueryRow(
			"SELECT refunded_count FROM trading_service_stats WHERE service_id = $1", svcID,
		).Scan(&refundedCount)
		if refundedCount >= 1 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.Equal(t, int32(1), refundedCount, "consumer should increment refunded_count within 5s")
}
