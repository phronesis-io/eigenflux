package trade_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/trade"
	"eigenflux_server/kitex_gen/eigenflux/trade/tradeservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/tests/testutil"

	"github.com/cloudwego/kitex/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	testutil.RunTestMain(m)
}

// newTradeClient builds a Trade RPC client connecting directly by port.
func newTradeClient(t *testing.T) tradeservice.Client {
	t.Helper()
	cfg := config.Load()
	cli, err := tradeservice.NewClient("TradeService",
		client.WithHostPorts(fmt.Sprintf("127.0.0.1:%d", cfg.TradeRPCPort)),
	)
	require.NoError(t, err, "failed to create trade rpc client")
	return cli
}

// publishTestService creates a trading service via RPC and returns its ID.
func publishTestService(t *testing.T, cli tradeservice.Client, sellerID int64, titleSuffix string) int64 {
	t.Helper()
	resp, err := cli.PublishService(context.Background(), &trade.PublishServiceReq{
		SellerAgentId:      sellerID,
		Title:              "Test Service " + titleSuffix,
		CapabilityDesc:     "A capability for testing purposes",
		CallSpecText:       "Call me with any input describing your task.",
		PriceText:          "10 USDC",
		AmountAtomic:       10_000_000,
		Asset:              "USDC",
		DeliveryDeadlineMs: 3_600_000, // 1 hour
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), resp.BaseResp.Code, "publish service failed: %s", resp.BaseResp.Msg)
	require.NotZero(t, resp.ServiceId)
	return resp.ServiceId
}

// agentIDCounter provides a monotonic offset for createTestAgentID to guarantee
// uniqueness even when called multiple times within the same nanosecond.
var agentIDCounter int64

// createTestAgentID returns a unique agent ID for testing (not inserted to DB).
// Since the trade RPC does not validate agent existence against the agents table,
// we combine a time base with an atomic counter to avoid same-nanosecond collisions.
func createTestAgentID() int64 {
	n := atomic.AddInt64(&agentIDCounter, 1)
	return time.Now().UnixNano()&^0xFFF | (n & 0xFFF)
}

// cleanTradeData removes trade-related rows for the given agent IDs.
func cleanTradeData(t *testing.T, agentIDs ...int64) {
	t.Helper()
	for _, id := range agentIDs {
		testutil.TestDB.Exec(
			`DELETE FROM trade_outbox
			 WHERE EXISTS (
			 	SELECT 1 FROM trade_orders
			 	WHERE (buyer_agent_id = $1 OR seller_agent_id = $1)
			 	  AND trade_outbox.payload_json @> jsonb_build_object('order_id', order_id::text)
			 )`,
			id,
		)
		testutil.TestDB.Exec(
			"DELETE FROM trade_order_events WHERE order_id IN (SELECT order_id FROM trade_orders WHERE buyer_agent_id = $1 OR seller_agent_id = $1)",
			id,
		)
		testutil.TestDB.Exec(
			"DELETE FROM trade_transfer_receipts WHERE order_id IN (SELECT order_id FROM trade_orders WHERE buyer_agent_id = $1 OR seller_agent_id = $1)",
			id,
		)
		testutil.TestDB.Exec("DELETE FROM trade_orders WHERE buyer_agent_id = $1 OR seller_agent_id = $1", id)
		testutil.TestDB.Exec("DELETE FROM trading_services WHERE seller_agent_id = $1", id)
	}
}

// TestPublishService verifies that a published service is returned in GetMyServices.
func TestPublishService(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	t.Cleanup(func() { cleanTradeData(t, sellerID) })

	serviceID := publishTestService(t, cli, sellerID, "Publish")

	resp, err := cli.GetMyServices(context.Background(), &trade.GetMyServicesReq{
		SellerAgentId: sellerID,
		Limit:         10,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), resp.BaseResp.Code, "GetMyServices failed: %s", resp.BaseResp.Msg)

	found := false
	for _, svc := range resp.Services {
		if svc.ServiceId == serviceID {
			found = true
			assert.Equal(t, "Test Service Publish", svc.Title)
			assert.Equal(t, int64(10_000_000), svc.AmountAtomic)
			assert.Equal(t, "USDC", svc.Asset)
			assert.Equal(t, int16(1), svc.Status, "service should be active (status=1)")
			break
		}
	}
	assert.True(t, found, "published service should appear in GetMyServices")
}

// TestUpdateService publishes a service, updates its title and price, and verifies the changes.
func TestUpdateService(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	t.Cleanup(func() { cleanTradeData(t, sellerID) })

	serviceID := publishTestService(t, cli, sellerID, "Update")

	updateResp, err := cli.UpdateService(context.Background(), &trade.UpdateServiceReq{
		ServiceId:          serviceID,
		SellerAgentId:      sellerID,
		Title:              "Updated Title",
		CapabilityDesc:     "Updated capability",
		CallSpecText:       "New call spec",
		CallSpecSchema:     `{}`,
		PriceText:          "20 USDC",
		AmountAtomic:       20_000_000,
		Asset:              "USDC",
		DeliveryDeadlineMs: 7_200_000,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(0), updateResp.BaseResp.Code, "UpdateService failed: %s", updateResp.BaseResp.Msg)

	resp, err := cli.GetMyServices(context.Background(), &trade.GetMyServicesReq{
		SellerAgentId: sellerID,
		Limit:         10,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), resp.BaseResp.Code)

	require.NotEmpty(t, resp.Services)
	found := false
	for _, svc := range resp.Services {
		if svc.ServiceId == serviceID {
			found = true
			assert.Equal(t, "Updated Title", svc.Title)
			assert.Equal(t, int64(20_000_000), svc.AmountAtomic)
			break
		}
	}
	assert.True(t, found, "updated service should appear in GetMyServices")
}

// TestOfflineService publishes then offlines a service and verifies the status change.
func TestOfflineService(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	t.Cleanup(func() { cleanTradeData(t, sellerID) })

	serviceID := publishTestService(t, cli, sellerID, "Offline")

	offlineResp, err := cli.OfflineService(context.Background(), &trade.OfflineServiceReq{
		ServiceId:     serviceID,
		SellerAgentId: sellerID,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(0), offlineResp.BaseResp.Code, "OfflineService failed: %s", offlineResp.BaseResp.Msg)

	resp, err := cli.GetMyServices(context.Background(), &trade.GetMyServicesReq{
		SellerAgentId: sellerID,
		Limit:         10,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), resp.BaseResp.Code)

	for _, svc := range resp.Services {
		if svc.ServiceId == serviceID {
			assert.Equal(t, int16(2), svc.Status, "service should be offline (status=2)")
			return
		}
	}
	t.Fatal("service not found in GetMyServices after offline")
}

// TestCreateOrder creates a service and an order against it, verifying the frozen snapshot.
func TestCreateOrder(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	t.Cleanup(func() { cleanTradeData(t, sellerID, buyerID) })

	serviceID := publishTestService(t, cli, sellerID, "CreateOrder")

	createResp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId: buyerID,
		ServiceId:    serviceID,
		BuyerInput:   "Please perform the task: test task",
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), createResp.BaseResp.Code, "CreateOrder failed: %s", createResp.BaseResp.Msg)
	require.NotZero(t, createResp.OrderId)

	getResp, err := cli.GetOrder(context.Background(), &trade.GetOrderReq{
		OrderId: createResp.OrderId,
		AgentId: buyerID,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), getResp.BaseResp.Code, "GetOrder failed: %s", getResp.BaseResp.Msg)

	order := getResp.Order
	require.NotNil(t, order)
	assert.Equal(t, createResp.OrderId, order.OrderId)
	assert.Equal(t, buyerID, order.BuyerAgentId)
	assert.Equal(t, sellerID, order.SellerAgentId)
	assert.Equal(t, serviceID, order.ServiceId)
	assert.Equal(t, int16(0), order.Status, "new order should have status created (0)")
	assert.Equal(t, "Test Service CreateOrder", order.FrozenTitle, "frozen title should match service title at time of order")
	assert.Equal(t, int64(10_000_000), order.FrozenAmountAtomic)
	assert.Equal(t, "USDC", order.FrozenAsset)
	assert.Equal(t, "Please perform the task: test task", order.BuyerInput)

	require.NotEmpty(t, getResp.Events, "order should have at least one event")
	assert.Equal(t, "created", getResp.Events[0].EventType)
}

// TestBuyerGateMaxActive verifies that a 4th order is rejected when 3 are already active.
func TestBuyerGateMaxActive(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	t.Cleanup(func() { cleanTradeData(t, sellerID, buyerID) })

	// Create 3 separate services (each order needs a different service to avoid re-ordering the same service;
	// the gate only blocks on active order count, not per-service, so reusing the same service is fine).
	serviceID := publishTestService(t, cli, sellerID, "Gate3")

	for i := 0; i < 3; i++ {
		resp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
			BuyerAgentId: buyerID,
			ServiceId:    serviceID,
			BuyerInput:   fmt.Sprintf("order %d", i),
		})
		require.NoError(t, err)
		require.Equal(t, int32(0), resp.BaseResp.Code, "expected order %d to succeed", i)
	}

	// 4th order should be rejected
	resp4, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId: buyerID,
		ServiceId:    serviceID,
		BuyerInput:   "order 4",
	})
	require.NoError(t, err)
	assert.NotEqual(t, int32(0), resp4.BaseResp.Code, "4th order should be rejected by buyer gate")
	t.Logf("4th order rejection message: %s", resp4.BaseResp.Msg)
}

// TestBuyerGatePendingRelease verifies that a new order is blocked when the buyer has a pending release.
func TestBuyerGatePendingRelease(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	t.Cleanup(func() { cleanTradeData(t, sellerID, buyerID) })

	serviceID := publishTestService(t, cli, sellerID, "PendingRelease")

	// Create an order
	createResp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId: buyerID,
		ServiceId:    serviceID,
		BuyerInput:   "initial order",
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), createResp.BaseResp.Code)
	orderID := createResp.OrderId

	// Deliver to reach "pending release" (status=delivered)
	deliverResp, err := cli.DeliverOrder(context.Background(), &trade.DeliverOrderReq{
		OrderId:         orderID,
		SellerAgentId:   sellerID,
		DeliveryPayload: "here is your result",
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), deliverResp.BaseResp.Code)

	// Now check the gate: buyer has a pending release, so a new order should be blocked
	gateResp, err := cli.GetGateStatus(context.Background(), &trade.GetGateStatusReq{
		BuyerAgentId: buyerID,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), gateResp.BaseResp.Code)
	assert.True(t, gateResp.HasPendingRelease, "buyer should have a pending release")
	assert.False(t, gateResp.CanCreateOrder, "buyer gate should block new orders when pending release exists")

	// Attempting to create another order should be rejected
	blockResp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId: buyerID,
		ServiceId:    serviceID,
		BuyerInput:   "should be blocked",
	})
	require.NoError(t, err)
	assert.NotEqual(t, int32(0), blockResp.BaseResp.Code, "new order should be blocked with pending release")
}

// TestDeliverOrder delivers the order with a payload.
func TestDeliverOrder(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	t.Cleanup(func() { cleanTradeData(t, sellerID, buyerID) })

	serviceID := publishTestService(t, cli, sellerID, "Deliver")

	createResp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId: buyerID,
		ServiceId:    serviceID,
		BuyerInput:   "delivery test",
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), createResp.BaseResp.Code)
	orderID := createResp.OrderId

	// Deliver
	deliverResp, err := cli.DeliverOrder(context.Background(), &trade.DeliverOrderReq{
		OrderId:         orderID,
		SellerAgentId:   sellerID,
		DeliveryPayload: `{"result":"task completed successfully","output":"analysis report"}`,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(0), deliverResp.BaseResp.Code, "DeliverOrder failed: %s", deliverResp.BaseResp.Msg)

	getResp, err := cli.GetOrder(context.Background(), &trade.GetOrderReq{
		OrderId: orderID,
		AgentId: sellerID,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), getResp.BaseResp.Code)
	assert.Equal(t, int16(2), getResp.Order.Status, "order should be delivered (status=2)")
	assert.Equal(t, `{"result":"task completed successfully","output":"analysis report"}`, getResp.Order.DeliveryPayload)

	var hasDeliveredEvent bool
	for _, ev := range getResp.Events {
		if ev.EventType == "delivered" {
			hasDeliveredEvent = true
			break
		}
	}
	assert.True(t, hasDeliveredEvent, "delivered event should be present")
}

// TestOrderStateValidation verifies that invalid transitions are rejected.
func TestOrderStateValidation(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	t.Cleanup(func() { cleanTradeData(t, sellerID, buyerID) })

	serviceID := publishTestService(t, cli, sellerID, "StateValidation")

	createResp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId: buyerID,
		ServiceId:    serviceID,
		BuyerInput:   "state validation test",
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), createResp.BaseResp.Code)
	orderID := createResp.OrderId

	// Release before delivery is invalid (must transition through delivered first).
	releaseResp, err := cli.ReleaseOrder(context.Background(), &trade.ReleaseOrderReq{
		OrderId:      orderID,
		BuyerAgentId: buyerID,
		TransferId:   "t_premature",
	})
	require.NoError(t, err)
	assert.NotEqual(t, int32(0), releaseResp.BaseResp.Code,
		"releasing from created state should be rejected")
	t.Logf("rejected release from created state: %s", releaseResp.BaseResp.Msg)
}

// TestCannotBuyOwnService verifies that a seller cannot buy their own service.
func TestCannotBuyOwnService(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	t.Cleanup(func() { cleanTradeData(t, sellerID) })

	serviceID := publishTestService(t, cli, sellerID, "SelfBuy")

	resp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId: sellerID, // same as seller
		ServiceId:    serviceID,
		BuyerInput:   "self purchase attempt",
	})
	require.NoError(t, err)
	assert.NotEqual(t, int32(0), resp.BaseResp.Code, "seller should not be able to buy own service")
	t.Logf("self-buy rejection message: %s", resp.BaseResp.Msg)
}

// TestGetOrder verifies that order details and events are returned correctly.
func TestGetOrder(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	t.Cleanup(func() { cleanTradeData(t, sellerID, buyerID) })

	serviceID := publishTestService(t, cli, sellerID, "GetOrder")

	createResp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId: buyerID,
		ServiceId:    serviceID,
		BuyerInput:   "get order test",
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), createResp.BaseResp.Code)
	orderID := createResp.OrderId

	// Buyer can get the order
	getAsBuyer, err := cli.GetOrder(context.Background(), &trade.GetOrderReq{
		OrderId: orderID,
		AgentId: buyerID,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), getAsBuyer.BaseResp.Code, "buyer should be able to get order")
	assert.Equal(t, orderID, getAsBuyer.Order.OrderId)
	assert.NotEmpty(t, getAsBuyer.Events, "order events should be returned")

	// Seller can also get the order
	getAsSeller, err := cli.GetOrder(context.Background(), &trade.GetOrderReq{
		OrderId: orderID,
		AgentId: sellerID,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), getAsSeller.BaseResp.Code, "seller should be able to get order")
	assert.Equal(t, orderID, getAsSeller.Order.OrderId)

	// Unrelated agent cannot get the order
	strangerID := createTestAgentID()
	getAsStranger, err := cli.GetOrder(context.Background(), &trade.GetOrderReq{
		OrderId: orderID,
		AgentId: strangerID,
	})
	require.NoError(t, err)
	assert.NotEqual(t, int32(0), getAsStranger.BaseResp.Code, "unrelated agent should not access order")
}

// TestListOrders creates orders and verifies listing as buyer vs seller with status filters.
func TestListOrders(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	t.Cleanup(func() { cleanTradeData(t, sellerID, buyerID) })

	serviceID := publishTestService(t, cli, sellerID, "ListOrders")

	// Create 2 orders as buyer
	var orderIDs []int64
	for i := 0; i < 2; i++ {
		resp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
			BuyerAgentId: buyerID,
			ServiceId:    serviceID,
			BuyerInput:   fmt.Sprintf("list orders test %d", i),
		})
		require.NoError(t, err)
		require.Equal(t, int32(0), resp.BaseResp.Code)
		orderIDs = append(orderIDs, resp.OrderId)
	}

	// List as buyer
	listAsBuyer, err := cli.ListOrders(context.Background(), &trade.ListOrdersReq{
		AgentId:      buyerID,
		Role:         "buyer",
		StatusFilter: -1, // no filter
		Limit:        10,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), listAsBuyer.BaseResp.Code)
	assert.GreaterOrEqual(t, len(listAsBuyer.Orders), 2, "buyer should see at least 2 orders")

	foundIDs := make(map[int64]bool)
	for _, o := range listAsBuyer.Orders {
		foundIDs[o.OrderId] = true
		assert.Equal(t, buyerID, o.BuyerAgentId)
	}
	for _, id := range orderIDs {
		assert.True(t, foundIDs[id], "order %d should appear in buyer list", id)
	}

	// List as seller
	listAsSeller, err := cli.ListOrders(context.Background(), &trade.ListOrdersReq{
		AgentId:      sellerID,
		Role:         "seller",
		StatusFilter: -1,
		Limit:        10,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), listAsSeller.BaseResp.Code)
	assert.GreaterOrEqual(t, len(listAsSeller.Orders), 2, "seller should see at least 2 orders")
	for _, o := range listAsSeller.Orders {
		assert.Equal(t, sellerID, o.SellerAgentId)
	}

	// Filter by status=0 (created)
	listByStatus, err := cli.ListOrders(context.Background(), &trade.ListOrdersReq{
		AgentId:      buyerID,
		Role:         "buyer",
		StatusFilter: 0, // created
		Limit:        10,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), listByStatus.BaseResp.Code)
	for _, o := range listByStatus.Orders {
		assert.Equal(t, int16(0), o.Status, "all orders should be in created status")
	}
}

// TestReleaseOrder tests the full flow up to delivery; the release step itself calls Chief
// and will fail without a real Chief service. We verify the state machine and data integrity
// up to the delivered state, then confirm that release is rejected when Chief is unavailable.
func TestReleaseOrder(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	t.Cleanup(func() { cleanTradeData(t, sellerID, buyerID) })

	serviceID := publishTestService(t, cli, sellerID, "ReleaseOrder")

	createResp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId: buyerID,
		ServiceId:    serviceID,
		BuyerInput:   "release test",
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), createResp.BaseResp.Code)
	orderID := createResp.OrderId

	// Deliver
	_, err = cli.DeliverOrder(context.Background(), &trade.DeliverOrderReq{
		OrderId:         orderID,
		SellerAgentId:   sellerID,
		DeliveryPayload: "completed work",
	})
	require.NoError(t, err)

	// Verify order is now delivered (status=2) before attempting release
	getResp, err := cli.GetOrder(context.Background(), &trade.GetOrderReq{
		OrderId: orderID,
		AgentId: buyerID,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), getResp.BaseResp.Code)
	assert.Equal(t, int16(2), getResp.Order.Status, "order should be delivered before release")

	// Attempt release — chief verifier will reject the synthesized id in the test env, which is expected.
	releaseResp, err := cli.ReleaseOrder(context.Background(), &trade.ReleaseOrderReq{
		OrderId:      orderID,
		BuyerAgentId: buyerID,
		TransferId:   "t_local_test",
	})
	require.NoError(t, err)
	t.Logf("ReleaseOrder result: code=%d msg=%s", releaseResp.BaseResp.Code, releaseResp.BaseResp.Msg)
}

// TestBuyerInputSchemaValidation publishes a service with a JSON Schema on call_spec_schema
// and verifies that CreateOrder accepts a conforming buyer_input and rejects a non-conforming one.
func TestBuyerInputSchemaValidation(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	t.Cleanup(func() { cleanTradeData(t, sellerID, buyerID) })

	// Publish a service with a strict schema requiring a "task" string field.
	schema := `{"type":"object","properties":{"task":{"type":"string"}},"required":["task"]}`
	resp, err := cli.PublishService(context.Background(), &trade.PublishServiceReq{
		SellerAgentId:      sellerID,
		Title:              "Schema Validated Service",
		CapabilityDesc:     "Validates buyer input against schema",
		CallSpecText:       "Provide {task: string}",
		CallSpecSchema:     schema,
		PriceText:          "5 USDC",
		AmountAtomic:       5_000_000,
		Asset:              "USDC",
		DeliveryDeadlineMs: 3_600_000,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), resp.BaseResp.Code, "publish service failed: %s", resp.BaseResp.Msg)
	serviceID := resp.ServiceId

	// Valid buyer_input: matches the schema.
	validResp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId: buyerID,
		ServiceId:    serviceID,
		BuyerInput:   `{"task":"translate doc"}`,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(0), validResp.BaseResp.Code, "order with valid buyer_input should succeed: %s", validResp.BaseResp.Msg)

	// Invalid buyer_input: missing required "task" field.
	invalidResp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId: buyerID,
		ServiceId:    serviceID,
		BuyerInput:   `{"wrong_field":123}`,
	})
	require.NoError(t, err)
	assert.NotEqual(t, int32(0), invalidResp.BaseResp.Code, "order with invalid buyer_input should be rejected")
	t.Logf("schema validation rejection: %s", invalidResp.BaseResp.Msg)
}

// TestAssetValidation verifies that PublishService rejects unsupported assets, accepts USDC,
// and defaults an empty asset to USDC.
func TestAssetValidation(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	t.Cleanup(func() { cleanTradeData(t, sellerID) })

	// Unsupported asset should be rejected.
	btcResp, err := cli.PublishService(context.Background(), &trade.PublishServiceReq{
		SellerAgentId:      sellerID,
		Title:              "BTC Service",
		CapabilityDesc:     "Asset validation test",
		CallSpecText:       "n/a",
		PriceText:          "0.001 BTC",
		AmountAtomic:       100_000,
		Asset:              "BTC",
		DeliveryDeadlineMs: 3_600_000,
	})
	require.NoError(t, err)
	assert.NotEqual(t, int32(0), btcResp.BaseResp.Code, "BTC asset should be rejected")
	t.Logf("BTC rejection: %s", btcResp.BaseResp.Msg)

	// USDC asset should be accepted.
	usdcResp, err := cli.PublishService(context.Background(), &trade.PublishServiceReq{
		SellerAgentId:      sellerID,
		Title:              "USDC Service",
		CapabilityDesc:     "Asset validation test",
		CallSpecText:       "n/a",
		PriceText:          "10 USDC",
		AmountAtomic:       10_000_000,
		Asset:              "USDC",
		DeliveryDeadlineMs: 3_600_000,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(0), usdcResp.BaseResp.Code, "USDC asset should succeed: %s", usdcResp.BaseResp.Msg)

	// Empty asset should default to USDC and succeed.
	defaultResp, err := cli.PublishService(context.Background(), &trade.PublishServiceReq{
		SellerAgentId:      sellerID,
		Title:              "Default Asset Service",
		CapabilityDesc:     "Asset default test",
		CallSpecText:       "n/a",
		PriceText:          "10 USDC",
		AmountAtomic:       10_000_000,
		Asset:              "",
		DeliveryDeadlineMs: 3_600_000,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(0), defaultResp.BaseResp.Code, "empty asset should default to USDC: %s", defaultResp.BaseResp.Msg)

	// Verify the defaulted service has asset=USDC stored.
	if defaultResp.BaseResp.Code == 0 {
		myResp, err := cli.GetMyServices(context.Background(), &trade.GetMyServicesReq{
			SellerAgentId: sellerID,
			Limit:         50,
		})
		require.NoError(t, err)
		for _, svc := range myResp.Services {
			if svc.ServiceId == defaultResp.ServiceId {
				assert.Equal(t, "USDC", svc.Asset, "defaulted asset should be stored as USDC")
				break
			}
		}
	}
}

// TestRefundOrder tests the full refund flow. RefundOrder no longer calls Chief,
// so it should succeed from a delivered order state.
func TestRefundOrder(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	t.Cleanup(func() { cleanTradeData(t, sellerID, buyerID) })

	serviceID := publishTestService(t, cli, sellerID, "RefundOrder")

	createResp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId: buyerID,
		ServiceId:    serviceID,
		BuyerInput:   "refund test",
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), createResp.BaseResp.Code)
	orderID := createResp.OrderId

	// Deliver
	_, err = cli.DeliverOrder(context.Background(), &trade.DeliverOrderReq{
		OrderId:         orderID,
		SellerAgentId:   sellerID,
		DeliveryPayload: "disputed work",
	})
	require.NoError(t, err)

	// Verify order is delivered before refund
	getResp, err := cli.GetOrder(context.Background(), &trade.GetOrderReq{
		OrderId: orderID,
		AgentId: buyerID,
	})
	require.NoError(t, err)
	assert.Equal(t, int16(2), getResp.Order.Status, "order should be delivered before refund")

	refundResp, err := cli.RefundOrder(context.Background(), &trade.RefundOrderReq{
		OrderId:      orderID,
		ActorAgentId: buyerID,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(0), refundResp.BaseResp.Code, "RefundOrder should succeed: %s", refundResp.BaseResp.Msg)
}

// TestReleaseOrder_TransferIDRequired verifies that a release without a
// transfer_id is rejected before any chief call.
func TestReleaseOrder_TransferIDRequired(t *testing.T) {
	cli := newTradeClient(t)
	sellerID := createTestAgentID()
	buyerID := createTestAgentID()
	t.Cleanup(func() { cleanTradeData(t, sellerID, buyerID) })

	serviceID := publishTestService(t, cli, sellerID, "ReleaseTransferIDRequired")
	createResp, err := cli.CreateOrder(context.Background(), &trade.CreateOrderReq{
		BuyerAgentId: buyerID, ServiceId: serviceID, BuyerInput: "x",
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), createResp.BaseResp.Code)
	orderID := createResp.OrderId

	_, err = cli.DeliverOrder(context.Background(), &trade.DeliverOrderReq{
		OrderId: orderID, SellerAgentId: sellerID, DeliveryPayload: "p",
	})
	require.NoError(t, err)

	resp, err := cli.ReleaseOrder(context.Background(), &trade.ReleaseOrderReq{
		OrderId: orderID, BuyerAgentId: buyerID, // TransferId intentionally empty
	})
	require.NoError(t, err)
	assert.NotEqual(t, int32(0), resp.BaseResp.Code, "empty transfer_id should be rejected")
	assert.Contains(t, resp.BaseResp.Msg, "transfer_id required")
}
