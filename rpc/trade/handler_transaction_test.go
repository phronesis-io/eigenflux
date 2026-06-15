package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/trade"
	"eigenflux_server/pkg/chief"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/rpc/trade/dal"
	"eigenflux_server/tests/testutil"

	"github.com/stretchr/testify/require"
)

type scriptedIDGen struct {
	ids []int64
	err error
}

func (g *scriptedIDGen) NextID() (int64, error) {
	if g.err != nil {
		return 0, g.err
	}
	if len(g.ids) == 0 {
		return 0, errors.New("scripted id exhausted")
	}
	id := g.ids[0]
	g.ids = g.ids[1:]
	return id, nil
}

var handlerTestIDCounter int64

func TestMain(m *testing.M) {
	testutil.InitDB()
	if _, err := testutil.TestDB.Exec("SELECT pg_advisory_lock($1)", int64(2026061201)); err != nil {
		panic("failed to acquire trade handler test lock: " + err.Error())
	}
	cfg := config.Load()
	db.Init(cfg.PgDSN)

	code := m.Run()

	_, _ = testutil.TestDB.Exec("SELECT pg_advisory_unlock($1)", int64(2026061201))
	os.Exit(code)
}

func nextHandlerTestID() int64 {
	n := atomic.AddInt64(&handlerTestIDCounter, 1)
	return time.Now().UnixNano()&^0xFFF | (n & 0xFFF)
}

func newHandlerTestService(t *testing.T) (serviceID, sellerID int64) {
	t.Helper()
	serviceID = nextHandlerTestID()
	sellerID = nextHandlerTestID()
	svc := &dal.TradingService{
		ServiceID:          serviceID,
		SellerAgentID:      sellerID,
		Title:              fmt.Sprintf("handler transaction %d", serviceID),
		CapabilityDesc:     "transaction test",
		CallSpecText:       "input text",
		PriceText:          "10 USDC",
		AmountAtomic:       10_000_000,
		Asset:              "USDC",
		DeliveryDeadlineMs: 3_600_000,
	}
	require.NoError(t, dal.CreateService(db.DB, svc))
	t.Cleanup(func() { cleanHandlerTradeRows(t, serviceID) })
	return serviceID, sellerID
}

func newHandlerTestOrder(t *testing.T, serviceID, sellerID, buyerID int64, status int16) int64 {
	t.Helper()
	orderID := nextHandlerTestID()
	order := &dal.TradeOrder{
		OrderID:                  orderID,
		ServiceID:                serviceID,
		BuyerAgentID:             buyerID,
		SellerAgentID:            sellerID,
		Status:                   status,
		FrozenTitle:              "handler transaction order",
		FrozenCallSpecText:       "input text",
		FrozenAmountAtomic:       10_000_000,
		FrozenAsset:              "USDC",
		FrozenDeliveryDeadlineMs: 3_600_000,
		BuyerInput:               "buyer input",
	}
	if status == dal.OrderStatusDelivered {
		now := time.Now().UnixMilli()
		order.DeliveryPayload = "delivered payload"
		order.DeliveredAt = &now
	}
	require.NoError(t, dal.CreateOrder(db.DB, order))
	t.Cleanup(func() { cleanHandlerOrderRows(t, orderID) })
	return orderID
}

func cleanHandlerTradeRows(t *testing.T, serviceID int64) {
	t.Helper()
	rows, err := testutil.TestDB.Query("SELECT order_id FROM trade_orders WHERE service_id = $1", serviceID)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var orderID int64
		require.NoError(t, rows.Scan(&orderID))
		cleanHandlerOrderRows(t, orderID)
	}
	require.NoError(t, rows.Err())
	_, _ = testutil.TestDB.Exec("DELETE FROM trading_service_stats WHERE service_id = $1", serviceID)
	_, _ = testutil.TestDB.Exec("DELETE FROM trading_services WHERE service_id = $1", serviceID)
}

func cleanHandlerOrderRows(t *testing.T, orderID int64) {
	t.Helper()
	filter := fmt.Sprintf(`{"order_id": "%d"}`, orderID)
	_, _ = testutil.TestDB.Exec("DELETE FROM trade_outbox WHERE payload_json @> $1::jsonb", filter)
	_, _ = testutil.TestDB.Exec("DELETE FROM trade_transfer_receipts WHERE order_id = $1", orderID)
	_, _ = testutil.TestDB.Exec("DELETE FROM trade_order_events WHERE order_id = $1", orderID)
	_, _ = testutil.TestDB.Exec("DELETE FROM trade_orders WHERE order_id = $1", orderID)
}

func countHandlerRows(t *testing.T, query string, args ...interface{}) int64 {
	t.Helper()
	var count int64
	require.NoError(t, testutil.TestDB.QueryRow(query, args...).Scan(&count))
	return count
}

func requireOrderStatus(t *testing.T, orderID int64, want int16) {
	t.Helper()
	var status int16
	require.NoError(t, testutil.TestDB.QueryRow(
		"SELECT status FROM trade_orders WHERE order_id = $1",
		orderID,
	).Scan(&status))
	require.Equal(t, want, status)
}

func TestCreateOrder_RollsBackWhenCreatedEventFails(t *testing.T) {
	serviceID, _ := newHandlerTestService(t)
	buyerID := nextHandlerTestID()
	orderID := nextHandlerTestID()
	key := fmt.Sprintf("rollback-create-%d", nextHandlerTestID())

	impl := &TradeServiceImpl{
		orderIDGen: &scriptedIDGen{ids: []int64{orderID}},
		eventIDGen: &scriptedIDGen{err: errors.New("event id boom")},
		maxActive:  3,
	}

	resp, err := impl.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId:   buyerID,
		ServiceId:      serviceID,
		BuyerInput:     "first attempt",
		IdempotencyKey: key,
	})
	require.NoError(t, err)
	require.NotEqual(t, int32(0), resp.BaseResp.Code)
	require.Equal(t, int64(0), countHandlerRows(t,
		"SELECT COUNT(*) FROM trade_orders WHERE order_id = $1 OR (buyer_agent_id = $2 AND idempotency_key = $3)",
		orderID, buyerID, key,
	))

	impl.orderIDGen = &scriptedIDGen{ids: []int64{nextHandlerTestID()}}
	impl.eventIDGen = &scriptedIDGen{ids: []int64{nextHandlerTestID()}}
	retryResp, err := impl.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId:   buyerID,
		ServiceId:      serviceID,
		BuyerInput:     "second attempt",
		IdempotencyKey: key,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), retryResp.BaseResp.Code, "retry after rollback should create a clean order: %s", retryResp.BaseResp.Msg)
	require.NotZero(t, retryResp.OrderId)
	require.Equal(t, int64(1), countHandlerRows(t,
		"SELECT COUNT(*) FROM trade_order_events WHERE order_id = $1 AND event_type = $2",
		retryResp.OrderId, dal.EventTypeCreated,
	))
}

func TestDeliverOrder_RollsBackStatusWhenEventFails(t *testing.T) {
	serviceID, sellerID := newHandlerTestService(t)
	buyerID := nextHandlerTestID()
	orderID := newHandlerTestOrder(t, serviceID, sellerID, buyerID, dal.OrderStatusCreated)

	impl := &TradeServiceImpl{
		eventIDGen: &scriptedIDGen{err: errors.New("event id boom")},
	}

	resp, err := impl.DeliverOrder(context.Background(), &trade.DeliverOrderReq{
		OrderId:         orderID,
		SellerAgentId:   sellerID,
		DeliveryPayload: "should roll back",
	})
	require.NoError(t, err)
	require.NotEqual(t, int32(0), resp.BaseResp.Code)
	requireOrderStatus(t, orderID, dal.OrderStatusCreated)

	var payload string
	var deliveredAt sql.NullInt64
	require.NoError(t, testutil.TestDB.QueryRow(
		"SELECT delivery_payload, delivered_at FROM trade_orders WHERE order_id = $1",
		orderID,
	).Scan(&payload, &deliveredAt))
	require.Empty(t, payload)
	require.False(t, deliveredAt.Valid)
	require.Equal(t, int64(0), countHandlerRows(t,
		"SELECT COUNT(*) FROM trade_order_events WHERE order_id = $1 AND event_type = $2",
		orderID, dal.EventTypeDelivered,
	))

	impl.eventIDGen = &scriptedIDGen{ids: []int64{nextHandlerTestID()}}
	retryResp, err := impl.DeliverOrder(context.Background(), &trade.DeliverOrderReq{
		OrderId:         orderID,
		SellerAgentId:   sellerID,
		DeliveryPayload: "delivered",
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), retryResp.BaseResp.Code)
	requireOrderStatus(t, orderID, dal.OrderStatusDelivered)
	require.Equal(t, int64(1), countHandlerRows(t,
		"SELECT COUNT(*) FROM trade_order_events WHERE order_id = $1 AND event_type = $2",
		orderID, dal.EventTypeDelivered,
	))
}

func TestRefundOrder_RollsBackWhenReceiptInsertFails(t *testing.T) {
	serviceID, sellerID := newHandlerTestService(t)
	buyerID := nextHandlerTestID()
	orderID := newHandlerTestOrder(t, serviceID, sellerID, buyerID, dal.OrderStatusDelivered)

	impl := &TradeServiceImpl{
		eventIDGen:   &scriptedIDGen{ids: []int64{nextHandlerTestID()}},
		receiptIDGen: &scriptedIDGen{err: errors.New("receipt id boom")},
		outboxIDGen:  &scriptedIDGen{ids: []int64{nextHandlerTestID()}},
	}

	resp, err := impl.RefundOrder(context.Background(), &trade.RefundOrderReq{
		OrderId:      orderID,
		ActorAgentId: buyerID,
	})
	require.NoError(t, err)
	require.NotEqual(t, int32(0), resp.BaseResp.Code)
	requireRefundRollback(t, orderID)
}

func TestRefundOrder_RollsBackWhenOutboxInsertFails(t *testing.T) {
	serviceID, sellerID := newHandlerTestService(t)
	buyerID := nextHandlerTestID()
	orderID := newHandlerTestOrder(t, serviceID, sellerID, buyerID, dal.OrderStatusDelivered)

	impl := &TradeServiceImpl{
		eventIDGen:   &scriptedIDGen{ids: []int64{nextHandlerTestID()}},
		receiptIDGen: &scriptedIDGen{ids: []int64{nextHandlerTestID()}},
		outboxIDGen:  &scriptedIDGen{err: errors.New("outbox id boom")},
	}

	resp, err := impl.RefundOrder(context.Background(), &trade.RefundOrderReq{
		OrderId:      orderID,
		ActorAgentId: buyerID,
	})
	require.NoError(t, err)
	require.NotEqual(t, int32(0), resp.BaseResp.Code)
	requireRefundRollback(t, orderID)
}

func requireRefundRollback(t *testing.T, orderID int64) {
	t.Helper()
	requireOrderStatus(t, orderID, dal.OrderStatusDelivered)
	require.Equal(t, int64(0), countHandlerRows(t,
		"SELECT COUNT(*) FROM trade_order_events WHERE order_id = $1 AND event_type = $2",
		orderID, dal.EventTypeRefunded,
	))
	require.Equal(t, int64(0), countHandlerRows(t,
		"SELECT COUNT(*) FROM trade_transfer_receipts WHERE order_id = $1 AND transfer_state = $2",
		orderID, "refunded",
	))
	filter := fmt.Sprintf(`{"order_id": "%d"}`, orderID)
	require.Equal(t, int64(0), countHandlerRows(t,
		"SELECT COUNT(*) FROM trade_outbox WHERE payload_json @> $1::jsonb",
		filter,
	))
}

func TestReleaseOrder_RetryDoesNotDuplicateSideEffects(t *testing.T) {
	serviceID, sellerID := newHandlerTestService(t)
	buyerID := int64(99)
	orderID := newHandlerTestOrder(t, serviceID, sellerID, buyerID, dal.OrderStatusDelivered)
	transferID := fmt.Sprintf("t_release_%d", nextHandlerTestID())

	chiefServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/ledger/entries", r.URL.Path)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"entries":[
			{"entryId":"e1","entryType":"agent_transfer","agentId":"%d","asset":"USDC","availableDeltaAtomic":"10000000",
			 "metadata":{"fromAgentId":"%d","toAgentId":"%d","transferId":"%s","transactionState":"SETTLED","txHash":"h","settlementRecordId":"s"},
			 "createdAt":"2026-06-12T00:00:00Z"}
		]}`, sellerID, buyerID, sellerID, transferID)))
	}))
	t.Cleanup(chiefServer.Close)

	impl := &TradeServiceImpl{
		eventIDGen:    &scriptedIDGen{ids: []int64{nextHandlerTestID()}},
		receiptIDGen:  &scriptedIDGen{ids: []int64{nextHandlerTestID()}},
		outboxIDGen:   &scriptedIDGen{ids: []int64{nextHandlerTestID()}},
		chiefClient:   chief.NewClient(chiefServer.URL, 2*time.Second),
		chiefLookback: 50,
	}

	resp, err := impl.ReleaseOrder(context.Background(), &trade.ReleaseOrderReq{
		OrderId:      orderID,
		BuyerAgentId: buyerID,
		TransferId:   transferID,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), resp.BaseResp.Code, "first release failed: %s", resp.BaseResp.Msg)
	retryResp, err := impl.ReleaseOrder(context.Background(), &trade.ReleaseOrderReq{
		OrderId:      orderID,
		BuyerAgentId: buyerID,
		TransferId:   transferID,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), retryResp.BaseResp.Code, "retry release should be idempotent: %s", retryResp.BaseResp.Msg)

	requireOrderStatus(t, orderID, dal.OrderStatusReleased)
	require.Equal(t, int64(1), countHandlerRows(t,
		"SELECT COUNT(*) FROM trade_order_events WHERE order_id = $1 AND event_type = $2",
		orderID, dal.EventTypeReleased,
	))
	require.Equal(t, int64(1), countHandlerRows(t,
		"SELECT COUNT(*) FROM trade_transfer_receipts WHERE order_id = $1 AND transfer_id = $2 AND transfer_state = $3",
		orderID, transferID, "released",
	))
	filter := fmt.Sprintf(`{"order_id": "%d", "event_type": "released"}`, orderID)
	require.Equal(t, int64(1), countHandlerRows(t,
		"SELECT COUNT(*) FROM trade_outbox WHERE payload_json @> $1::jsonb",
		filter,
	))
}
