package trade_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/trade"
	"eigenflux_server/tests/testutil"

	"github.com/stretchr/testify/require"
)

func TestCreateOrder_ConcurrentSameIdempotencyKey(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	defer cleanTradeData(t, sellerID, buyerID)

	svcID := publishTestService(t, cli, sellerID, "idemp-same")

	key := fmt.Sprintf("idemp-%d", time.Now().UnixNano())

	const N = 10
	var wg sync.WaitGroup
	resultIDs := make([]int64, N)
	codes := make([]int32, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
				BuyerAgentId:   buyerID,
				ServiceId:      svcID,
				BuyerInput:     "test",
				IdempotencyKey: key,
			})
			require.NoError(t, err)
			resultIDs[i] = resp.OrderId
			codes[i] = resp.BaseResp.Code
		}(i)
	}
	wg.Wait()

	// All 10 calls must succeed (code 0) and return the same order_id.
	for i := 0; i < N; i++ {
		require.Equal(t, int32(0), codes[i], "call %d failed", i)
	}
	first := resultIDs[0]
	require.NotZero(t, first)
	for i := 1; i < N; i++ {
		require.Equal(t, first, resultIDs[i], "all 10 calls must return same order_id")
	}

	var count int64
	require.NoError(t, testutil.TestDB.QueryRow(
		"SELECT COUNT(*) FROM trade_orders WHERE buyer_agent_id = $1 AND idempotency_key = $2",
		buyerID, key,
	).Scan(&count))
	require.Equal(t, int64(1), count, "exactly one trade_orders row")

	require.NoError(t, testutil.TestDB.QueryRow(
		"SELECT COUNT(*) FROM trade_order_events WHERE order_id = $1 AND event_type = $2",
		first, int16(1), // EventTypeCreated
	).Scan(&count))
	require.Equal(t, int64(1), count, "exactly one created event row")
}

func TestCreateOrder_GateRaceFreeWithDifferentKeys(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	defer cleanTradeData(t, sellerID, buyerID)

	svcID := publishTestService(t, cli, sellerID, "gate-race")

	// max_active default is 3 (TRADE_MAX_ACTIVE_ORDERS). Fire 5 concurrent
	// creates with different keys; assert exactly 3 succeed.
	const N = 5
	var wg sync.WaitGroup
	codes := make([]int32, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
				BuyerAgentId:   buyerID,
				ServiceId:      svcID,
				BuyerInput:     "x",
				IdempotencyKey: fmt.Sprintf("gate-race-%d-%d", time.Now().UnixNano(), i),
			})
			if err != nil {
				codes[i] = -1
				return
			}
			codes[i] = resp.BaseResp.Code
		}(i)
	}
	wg.Wait()

	successCount := 0
	for _, c := range codes {
		if c == 0 {
			successCount++
		}
	}
	require.Equal(t, 3, successCount, "exactly TRADE_MAX_ACTIVE_ORDERS=3 should succeed, got %d", successCount)
}
