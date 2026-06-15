package e2e_test

import (
	"fmt"
	"testing"
	"time"

	"eigenflux_server/tests/testutil"
)

// Order status constants mirror rpc/trade/dal.OrderStatus*. Duplicated here so
// the e2e package does not have to import the trade DAL (which would pull in
// gorm and DB wiring just to read a few ints).
const (
	orderStatusCreated   = 0
	orderStatusDelivered = 2
	orderStatusRefunded  = 6

	tradingServiceStatusActive  = 1
	tradingServiceStatusOffline = 2
)

// gateStatus reads /api/v1/trading/gate and returns the four reported fields.
// Returns -1 in any int field if the response shape is unexpected so the
// caller can surface the failure with the raw body.
func gateStatus(t *testing.T, token string) (canCreate, hasPendingRelease bool, activeCount, maxActive int) {
	t.Helper()
	resp := testutil.DoGet(t, "/api/v1/trading/gate", token)
	if code, _ := resp["code"].(float64); code != 0 {
		t.Fatalf("gate status returned non-zero code: %#v", resp)
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("gate response missing data: %#v", resp)
	}
	canCreate, _ = data["can_create_order"].(bool)
	hasPendingRelease, _ = data["has_pending_release"].(bool)
	if v, ok := data["active_order_count"].(float64); ok {
		activeCount = int(v)
	} else {
		activeCount = -1
	}
	if v, ok := data["max_active_orders"].(float64); ok {
		maxActive = int(v)
	} else {
		maxActive = -1
	}
	return
}

// createOrderViaHTTP wraps POST /api/v1/trading/orders, requires success, and
// returns the order_id string from the response payload.
func createOrderViaHTTP(t *testing.T, buyerToken string, serviceID int64, buyerInput string) string {
	t.Helper()
	resp := testutil.DoPost(t, "/api/v1/trading/orders",
		map[string]interface{}{
			"service_id":  serviceID,
			"buyer_input": buyerInput,
		},
		buyerToken,
	)
	if code, _ := resp["code"].(float64); code != 0 {
		t.Fatalf("create order failed: %#v", resp)
	}
	data, _ := resp["data"].(map[string]interface{})
	id, _ := data["order_id"].(string)
	if id == "" {
		t.Fatalf("create order response missing order_id: %#v", resp)
	}
	return id
}

// deliverOrderViaHTTP wraps POST /api/v1/trading/orders/:id/deliver.
func deliverOrderViaHTTP(t *testing.T, sellerToken, orderID, payload string) {
	t.Helper()
	resp := testutil.DoPost(t,
		fmt.Sprintf("/api/v1/trading/orders/%s/deliver", orderID),
		map[string]interface{}{"delivery_payload": payload},
		sellerToken,
	)
	if code, _ := resp["code"].(float64); code != 0 {
		t.Fatalf("deliver failed: %#v", resp)
	}
}

// getOrderViaHTTP wraps GET /api/v1/trading/orders/:id and returns the
// `order` and `events` maps. Bare nil values from a missing field surface as
// empty maps so callers can still index with the `_` ok pattern.
func getOrderViaHTTP(t *testing.T, token, orderID string) (map[string]interface{}, []interface{}) {
	t.Helper()
	resp := testutil.DoGet(t, "/api/v1/trading/orders/"+orderID, token)
	if code, _ := resp["code"].(float64); code != 0 {
		t.Fatalf("get order failed: %#v", resp)
	}
	data, _ := resp["data"].(map[string]interface{})
	order, _ := data["order"].(map[string]interface{})
	events, _ := data["events"].([]interface{})
	return order, events
}

// TestTradingGateStatusReflectsLifecycle drives the buyer gate through three
// transitions and asserts /api/v1/trading/gate reports them correctly:
//
//	t=0       active=0, can_create=true,  has_pending_release=false
//	t=create  active=1, can_create=true,  has_pending_release=false
//	t=deliver active=1, can_create=false, has_pending_release=true
//	t=refund  active=0, can_create=true,  has_pending_release=false
//
// Refund is used (not release) because release calls into the Chief verifier,
// which rejects synthesized transfer_ids in the local test environment.
func TestTradingGateStatusReflectsLifecycle(t *testing.T) {
	testutil.WaitForAPI(t)

	sellerEmail := fmt.Sprintf("trade_gate_seller_%d@test.com", time.Now().UnixNano())
	buyerEmail := fmt.Sprintf("trade_gate_buyer_%d@test.com", time.Now().UnixNano())
	sellerReg := testutil.RegisterAgent(t, sellerEmail, "GateSeller", "gate seller")
	buyerReg := testutil.RegisterAgent(t, buyerEmail, "GateBuyer", "gate buyer")
	t.Cleanup(func() { testutil.CleanupTestEmails(t, sellerEmail, buyerEmail) })
	sellerToken := sellerReg["token"].(string)
	buyerToken := buyerReg["token"].(string)

	// t=0
	canCreate, hasPending, active, maxActive := gateStatus(t, buyerToken)
	if !canCreate || hasPending || active != 0 {
		t.Fatalf("initial gate: want can_create=true active=0 pending=false; got can=%v active=%d pending=%v",
			canCreate, active, hasPending)
	}
	if maxActive <= 0 {
		t.Fatalf("max_active_orders must be positive, got %d", maxActive)
	}

	serviceID := publishTradingServiceForE2E(t, sellerToken, "Gate lifecycle seed", "")
	orderID := createOrderViaHTTP(t, buyerToken, serviceID, `{"note":"gate test"}`)

	// t=create: one active, still under cap, no pending release
	canCreate, hasPending, active, _ = gateStatus(t, buyerToken)
	if !canCreate || hasPending || active != 1 {
		t.Fatalf("after create: want can_create=true active=1 pending=false; got can=%v active=%d pending=%v",
			canCreate, active, hasPending)
	}

	deliverOrderViaHTTP(t, sellerToken, orderID, "delivery payload")

	// t=deliver: still one active, but has_pending_release blocks further creates
	canCreate, hasPending, active, _ = gateStatus(t, buyerToken)
	if canCreate || !hasPending || active != 1 {
		t.Fatalf("after deliver: want can_create=false active=1 pending=true; got can=%v active=%d pending=%v",
			canCreate, active, hasPending)
	}

	// Refund frees the gate (release would call Chief, which rejects in test env)
	refundResp := testutil.DoPost(t,
		fmt.Sprintf("/api/v1/trading/orders/%s/refund", orderID),
		map[string]interface{}{},
		buyerToken,
	)
	if code, _ := refundResp["code"].(float64); code != 0 {
		t.Fatalf("refund failed: %#v", refundResp)
	}

	canCreate, hasPending, active, _ = gateStatus(t, buyerToken)
	if !canCreate || hasPending || active != 0 {
		t.Fatalf("after refund: want can_create=true active=0 pending=false; got can=%v active=%d pending=%v",
			canCreate, active, hasPending)
	}
}

// TestTradingRefundFromDeliveredViaHTTP exercises POST /api/v1/trading/orders/:id/refund
// from a DELIVERED order and verifies the order ends up in status=6 with
// refunded_at populated and a "refunded" event emitted. This is the
// HTTP-layer complement to TestRefundOrder which goes via the RPC client.
func TestTradingRefundFromDeliveredViaHTTP(t *testing.T) {
	testutil.WaitForAPI(t)

	sellerEmail := fmt.Sprintf("trade_refund_seller_%d@test.com", time.Now().UnixNano())
	buyerEmail := fmt.Sprintf("trade_refund_buyer_%d@test.com", time.Now().UnixNano())
	sellerReg := testutil.RegisterAgent(t, sellerEmail, "RefundSeller", "refund seller")
	buyerReg := testutil.RegisterAgent(t, buyerEmail, "RefundBuyer", "refund buyer")
	t.Cleanup(func() { testutil.CleanupTestEmails(t, sellerEmail, buyerEmail) })
	sellerToken := sellerReg["token"].(string)
	buyerToken := buyerReg["token"].(string)

	serviceID := publishTradingServiceForE2E(t, sellerToken, "Refund flow seed", "")
	orderID := createOrderViaHTTP(t, buyerToken, serviceID, `{"note":"refund test"}`)
	deliverOrderViaHTTP(t, sellerToken, orderID, "to be refunded")

	// Sanity: order is delivered before refund.
	preOrder, _ := getOrderViaHTTP(t, buyerToken, orderID)
	if got, _ := preOrder["status"].(float64); int(got) != orderStatusDelivered {
		t.Fatalf("pre-refund order must be delivered (status=%d); got status=%v",
			orderStatusDelivered, preOrder["status"])
	}

	refundResp := testutil.DoPost(t,
		fmt.Sprintf("/api/v1/trading/orders/%s/refund", orderID),
		map[string]interface{}{},
		buyerToken,
	)
	if code, _ := refundResp["code"].(float64); code != 0 {
		t.Fatalf("refund failed: %#v", refundResp)
	}

	postOrder, events := getOrderViaHTTP(t, buyerToken, orderID)
	if got, _ := postOrder["status"].(float64); int(got) != orderStatusRefunded {
		t.Fatalf("post-refund order status = %v, want %d", postOrder["status"], orderStatusRefunded)
	}
	if got, _ := postOrder["refunded_at"].(float64); got == 0 {
		t.Fatalf("post-refund order missing refunded_at: %#v", postOrder)
	}

	var sawRefunded bool
	for _, raw := range events {
		ev, _ := raw.(map[string]interface{})
		if t2, _ := ev["event_type"].(string); t2 == "refunded" {
			sawRefunded = true
			break
		}
	}
	if !sawRefunded {
		t.Fatalf("expected a refunded event after refund; got events=%v", events)
	}

	// Refund is idempotent at terminal status: a duplicate call returns
	// success and must not move the order out of REFUNDED nor emit a second
	// `refunded` event. Verifying this here matters because RefundTradeOrder
	// is reachable from retry-prone client paths.
	dupResp := testutil.DoPost(t,
		fmt.Sprintf("/api/v1/trading/orders/%s/refund", orderID),
		map[string]interface{}{},
		buyerToken,
	)
	if code, _ := dupResp["code"].(float64); code != 0 {
		t.Fatalf("second refund should be idempotent (code=0); got: %#v", dupResp)
	}
	dupOrder, dupEvents := getOrderViaHTTP(t, buyerToken, orderID)
	if got, _ := dupOrder["status"].(float64); int(got) != orderStatusRefunded {
		t.Fatalf("after idempotent refund, status must remain %d; got %v",
			orderStatusRefunded, dupOrder["status"])
	}
	var refundedCount int
	for _, raw := range dupEvents {
		ev, _ := raw.(map[string]interface{})
		if t2, _ := ev["event_type"].(string); t2 == "refunded" {
			refundedCount++
		}
	}
	if refundedCount != 1 {
		t.Fatalf("idempotent refund must not emit a second `refunded` event; got %d", refundedCount)
	}
}

// TestTradingOfflineServiceViaHTTP verifies that POST .../services/:id/offline
// flips the service status to Offline and that subsequent CreateOrder against
// the same service is rejected (services must be Active to be ordered).
func TestTradingOfflineServiceViaHTTP(t *testing.T) {
	testutil.WaitForAPI(t)

	sellerEmail := fmt.Sprintf("trade_offline_seller_%d@test.com", time.Now().UnixNano())
	buyerEmail := fmt.Sprintf("trade_offline_buyer_%d@test.com", time.Now().UnixNano())
	sellerReg := testutil.RegisterAgent(t, sellerEmail, "OfflineSeller", "offline seller")
	buyerReg := testutil.RegisterAgent(t, buyerEmail, "OfflineBuyer", "offline buyer")
	t.Cleanup(func() { testutil.CleanupTestEmails(t, sellerEmail, buyerEmail) })
	sellerToken := sellerReg["token"].(string)
	buyerToken := buyerReg["token"].(string)

	serviceID := publishTradingServiceForE2E(t, sellerToken, "Offline flow seed", "")

	// Sanity: GetMyTradingServices reports Active.
	listResp := testutil.DoGet(t, "/api/v1/trading/services/me?limit=50", sellerToken)
	listData, _ := listResp["data"].(map[string]interface{})
	services, _ := listData["services"].([]interface{})
	status := serviceStatusInList(services, serviceID)
	if status != tradingServiceStatusActive {
		t.Fatalf("seed service must be Active; got status=%d", status)
	}

	offlineResp := testutil.DoPost(t,
		fmt.Sprintf("/api/v1/trading/services/%d/offline", serviceID),
		map[string]interface{}{},
		sellerToken,
	)
	if code, _ := offlineResp["code"].(float64); code != 0 {
		t.Fatalf("offline service failed: %#v", offlineResp)
	}

	// Status flips to Offline.
	listResp2 := testutil.DoGet(t, "/api/v1/trading/services/me?limit=50", sellerToken)
	listData2, _ := listResp2["data"].(map[string]interface{})
	services2, _ := listData2["services"].([]interface{})
	if got := serviceStatusInList(services2, serviceID); got != tradingServiceStatusOffline {
		t.Fatalf("post-offline status = %d, want %d", got, tradingServiceStatusOffline)
	}

	// CreateOrder against an offline service is rejected by the handler.
	createResp := testutil.DoPost(t, "/api/v1/trading/orders",
		map[string]interface{}{
			"service_id":  serviceID,
			"buyer_input": `{"note":"offline create"}`,
		},
		buyerToken,
	)
	if code, _ := createResp["code"].(float64); code == 0 {
		t.Fatalf("creating an order against an Offline service must be rejected: %#v", createResp)
	}
}

// serviceStatusInList looks up the status field of one service in a
// GetMyTradingServices response. Returns -1 when the id is not in the list.
func serviceStatusInList(services []interface{}, wantID int64) int {
	wantIDStr := fmt.Sprintf("%d", wantID)
	for _, raw := range services {
		svc, _ := raw.(map[string]interface{})
		if idStr, _ := svc["service_id"].(string); idStr == wantIDStr {
			if v, ok := svc["status"].(float64); ok {
				return int(v)
			}
			return -1
		}
	}
	return -1
}

// TestTradingListOrdersFilteringViaHTTP exercises GET /api/v1/trading/orders
// with role=buyer / role=seller and confirms each side sees only the orders
// that match its role. Also checks the status filter for status=0 (created)
// returns only created-state orders.
func TestTradingListOrdersFilteringViaHTTP(t *testing.T) {
	testutil.WaitForAPI(t)

	sellerEmail := fmt.Sprintf("trade_list_seller_%d@test.com", time.Now().UnixNano())
	buyerEmail := fmt.Sprintf("trade_list_buyer_%d@test.com", time.Now().UnixNano())
	sellerReg := testutil.RegisterAgent(t, sellerEmail, "ListSeller", "list seller")
	buyerReg := testutil.RegisterAgent(t, buyerEmail, "ListBuyer", "list buyer")
	t.Cleanup(func() { testutil.CleanupTestEmails(t, sellerEmail, buyerEmail) })
	sellerToken := sellerReg["token"].(string)
	buyerToken := buyerReg["token"].(string)
	sellerAgentIDStr := sellerReg["agent_id"].(string)
	buyerAgentIDStr := buyerReg["agent_id"].(string)

	svc1 := publishTradingServiceForE2E(t, sellerToken, "List seed A", "")
	svc2 := publishTradingServiceForE2E(t, sellerToken, "List seed B", "")
	order1 := createOrderViaHTTP(t, buyerToken, svc1, `{"note":"order 1"}`)
	order2 := createOrderViaHTTP(t, buyerToken, svc2, `{"note":"order 2"}`)

	// Buyer role view: must contain both orders and every row must carry the
	// buyer's agent id.
	buyerList := testutil.DoGet(t, "/api/v1/trading/orders?role=buyer&limit=50", buyerToken)
	if code, _ := buyerList["code"].(float64); code != 0 {
		t.Fatalf("list orders (buyer role) failed: %#v", buyerList)
	}
	buyerData, _ := buyerList["data"].(map[string]interface{})
	buyerOrders, _ := buyerData["orders"].([]interface{})
	if len(buyerOrders) < 2 {
		t.Fatalf("buyer should see at least 2 orders, got %d", len(buyerOrders))
	}
	for _, raw := range buyerOrders {
		o, _ := raw.(map[string]interface{})
		if got, _ := o["buyer_agent_id"].(string); got != buyerAgentIDStr {
			t.Fatalf("buyer-role list returned order whose buyer_agent_id=%q does not match caller %q",
				got, buyerAgentIDStr)
		}
	}
	if !containsOrderID(buyerOrders, order1) || !containsOrderID(buyerOrders, order2) {
		t.Fatalf("buyer-role list missing one of (%s, %s); got orders=%v", order1, order2, buyerOrders)
	}

	// Seller role view: each row carries the seller's id.
	sellerList := testutil.DoGet(t, "/api/v1/trading/orders?role=seller&limit=50", sellerToken)
	if code, _ := sellerList["code"].(float64); code != 0 {
		t.Fatalf("list orders (seller role) failed: %#v", sellerList)
	}
	sellerData, _ := sellerList["data"].(map[string]interface{})
	sellerOrders, _ := sellerData["orders"].([]interface{})
	if len(sellerOrders) < 2 {
		t.Fatalf("seller should see at least 2 orders, got %d", len(sellerOrders))
	}
	for _, raw := range sellerOrders {
		o, _ := raw.(map[string]interface{})
		if got, _ := o["seller_agent_id"].(string); got != sellerAgentIDStr {
			t.Fatalf("seller-role list returned order whose seller_agent_id=%q does not match caller %q",
				got, sellerAgentIDStr)
		}
	}

	// Deliver one order. Status filter for created (status=0) must return only
	// the still-created order; the delivered one must drop out.
	deliverOrderViaHTTP(t, sellerToken, order1, "delivered for filter test")

	filtered := testutil.DoGet(t,
		"/api/v1/trading/orders?role=buyer&status=0&limit=50",
		buyerToken,
	)
	if code, _ := filtered["code"].(float64); code != 0 {
		t.Fatalf("list orders status=0 failed: %#v", filtered)
	}
	filteredData, _ := filtered["data"].(map[string]interface{})
	filteredOrders, _ := filteredData["orders"].([]interface{})
	for _, raw := range filteredOrders {
		o, _ := raw.(map[string]interface{})
		if got, _ := o["status"].(float64); int(got) != orderStatusCreated {
			t.Fatalf("status=0 filter returned order with status=%v", o["status"])
		}
	}
	if containsOrderID(filteredOrders, order1) {
		t.Fatalf("status=0 filter must not return the delivered order %s", order1)
	}
	if !containsOrderID(filteredOrders, order2) {
		t.Fatalf("status=0 filter should still include the un-delivered order %s", order2)
	}
}

func containsOrderID(orders []interface{}, want string) bool {
	for _, raw := range orders {
		o, _ := raw.(map[string]interface{})
		if got, _ := o["order_id"].(string); got == want {
			return true
		}
	}
	return false
}

// TestTradingSearchServicesViaHTTP exercises POST /api/v1/trading/services/search
// at the HTTP layer. It covers:
//   - empty raw_query is rejected with a non-zero code from the gateway, and
//   - a valid multi-intent query returns a well-formed debug envelope and
//     results array (number of hits depends on enrichment so we don't assert
//     count, only shape and that the debug echoes the agent's sub_intents).
func TestTradingSearchServicesViaHTTP(t *testing.T) {
	testutil.WaitForAPI(t)

	email := fmt.Sprintf("trade_search_%d@test.com", time.Now().UnixNano())
	reg := testutil.RegisterAgent(t, email, "SearchTrader", "trade search caller")
	t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })
	token := reg["token"].(string)

	// Reject path: empty raw_query.
	rejectResp := testutil.DoPost(t, "/api/v1/trading/services/search",
		map[string]interface{}{
			"raw_query": "",
		},
		token,
	)
	if code, _ := rejectResp["code"].(float64); code == 0 {
		t.Fatalf("empty raw_query must be rejected: %#v", rejectResp)
	}

	// Happy path: a real query. We don't gate on result count because the
	// ranker's recall depends on enrichment_version, which only advances after
	// the LLM enrichment pipeline catches up. The contract we DO assert is
	// that the debug envelope is well-formed.
	resp := testutil.DoPost(t, "/api/v1/trading/services/search",
		map[string]interface{}{
			"raw_query": "Translate Spanish document to Chinese and generate slides.",
			"sub_intents": []map[string]interface{}{
				{"name": "translate", "query_text": "Spanish to Chinese translation", "importance": 1.0},
				{"name": "slides", "query_text": "Generate slide deck from outline", "importance": 0.8},
			},
			"limit": 10,
		},
		token,
	)
	if code, _ := resp["code"].(float64); code != 0 {
		t.Fatalf("search trading services failed: %#v", resp)
	}
	data, _ := resp["data"].(map[string]interface{})
	if _, ok := data["results"].([]interface{}); !ok {
		t.Fatalf("response.data.results missing or wrong type: %#v", data)
	}
	debug, ok := data["debug"].(map[string]interface{})
	if !ok {
		t.Fatalf("response.data.debug missing or wrong type: %#v", data)
	}
	if got, _ := debug["sub_intents_source"].(string); got != "agent" {
		t.Fatalf("debug.sub_intents_source = %q, want \"agent\"", got)
	}
	effective, _ := debug["effective_sub_intents"].([]interface{})
	if len(effective) != 2 {
		t.Fatalf("debug.effective_sub_intents should echo the 2 sub_intents we sent; got %d", len(effective))
	}
}
