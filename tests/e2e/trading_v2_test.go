package e2e_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"eigenflux_server/tests/testutil"
)

// publishTradingServiceForE2E publishes a trading service via the HTTP
// gateway and returns its numeric service_id. Used by V2 tests that need a
// seed service to mutate or order against.
func publishTradingServiceForE2E(t *testing.T, token, title string, schema string) int64 {
	t.Helper()
	// call_spec_schema is jsonb in PostgreSQL; an empty string fails the
	// type cast. Default to an empty JSON object when the caller does not
	// want to exercise schema validation.
	if schema == "" {
		schema = `{}`
	}
	body := map[string]interface{}{
		"title":                title,
		"capability_desc":      "e2e trading capability",
		"call_spec_text":       "Call spec",
		"call_spec_schema":     schema,
		"price_text":           "10 USDC",
		"amount_atomic":        10_000_000,
		"asset":                "USDC",
		"delivery_deadline_ms": 3_600_000,
	}
	resp := testutil.DoPost(t, "/api/v1/trading/services", body, token)
	code, _ := resp["code"].(float64)
	if code != 0 {
		t.Fatalf("publish failed: code=%v msg=%v", resp["code"], resp["msg"])
	}
	data, _ := resp["data"].(map[string]interface{})
	idStr, _ := data["service_id"].(string)
	if idStr == "" {
		t.Fatalf("publish response missing data.service_id: %#v", resp)
	}
	var id int64
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil || id == 0 {
		t.Fatalf("invalid service_id %q in publish response", idStr)
	}
	return id
}

// TestTradingUpdateRejectsUnsupportedAsset exercises the asset whitelist on
// the HTTP PUT path. The seed service is published with USDC; the update
// attempts to switch to JPY and must be rejected before any DB write.
// Verified end-to-end: response shows non-zero code, the row's asset stays
// USDC.
func TestTradingUpdateRejectsUnsupportedAsset(t *testing.T) {
	testutil.WaitForAPI(t)

	email := fmt.Sprintf("trade_v2_asset_%d@test.com", time.Now().UnixNano())
	reg := testutil.RegisterAgent(t, email, "TradeV2Asset", "trading v2 asset whitelist test")
	t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })
	token := reg["token"].(string)

	serviceID := publishTradingServiceForE2E(t, token, "Asset whitelist seed", "")

	updateResp := testutil.DoPut(t,
		fmt.Sprintf("/api/v1/trading/services/%d", serviceID),
		map[string]interface{}{
			"title":                "Trying to switch to JPY",
			"capability_desc":      "should be rejected pre-DB",
			"call_spec_text":       "spec",
			"price_text":           "1000 JPY",
			"amount_atomic":        1_000_000,
			"asset":                "JPY",
			"delivery_deadline_ms": 3_600_000,
		},
		token,
	)

	if code, _ := updateResp["code"].(float64); code == 0 {
		t.Fatalf("expected non-zero code rejecting JPY asset, got: %#v", updateResp)
	}
	if msg, _ := updateResp["msg"].(string); msg != "" {
		t.Logf("rejection msg: %s", msg)
	}

	listResp := testutil.DoGet(t, "/api/v1/trading/services/me?limit=50", token)
	data, _ := listResp["data"].(map[string]interface{})
	services, _ := data["services"].([]interface{})
	var foundAsset string
	for _, raw := range services {
		svc, _ := raw.(map[string]interface{})
		if idStr, _ := svc["service_id"].(string); idStr == fmt.Sprintf("%d", serviceID) {
			foundAsset, _ = svc["asset"].(string)
			break
		}
	}
	if foundAsset != "USDC" {
		t.Fatalf("seed service asset must not be overwritten by a rejected update; got %q", foundAsset)
	}
}

// TestTradingBuyerInputSchemaTypeMismatch covers the gojsonschema
// type-mismatch branch (CreateOrder with a string-typed schema field given a
// numeric value). Existing TestBuyerInputSchemaValidation at the RPC layer
// covers the missing-required-field branch only. This test goes through the
// HTTP gateway so the response code surfaces from the actual handler.
func TestTradingBuyerInputSchemaTypeMismatch(t *testing.T) {
	testutil.WaitForAPI(t)

	sellerEmail := fmt.Sprintf("trade_v2_schema_seller_%d@test.com", time.Now().UnixNano())
	buyerEmail := fmt.Sprintf("trade_v2_schema_buyer_%d@test.com", time.Now().UnixNano())
	sellerReg := testutil.RegisterAgent(t, sellerEmail, "SchemaSeller", "v2 schema seller")
	buyerReg := testutil.RegisterAgent(t, buyerEmail, "SchemaBuyer", "v2 schema buyer")
	t.Cleanup(func() { testutil.CleanupTestEmails(t, sellerEmail, buyerEmail) })
	sellerToken := sellerReg["token"].(string)
	buyerToken := buyerReg["token"].(string)

	schema := `{"type":"object","required":["task"],"properties":{"task":{"type":"string"}}}`
	serviceID := publishTradingServiceForE2E(t, sellerToken, "Schema validated service", schema)

	resp := testutil.DoPost(t, "/api/v1/trading/orders",
		map[string]interface{}{
			"service_id":  serviceID,
			"buyer_input": `{"task":42}`,
		},
		buyerToken,
	)
	code, _ := resp["code"].(float64)
	if code == 0 {
		t.Fatalf("expected non-zero code rejecting type-mismatched buyer_input, got: %#v", resp)
	}
	msg, _ := resp["msg"].(string)
	if msg != "" {
		t.Logf("rejection msg: %s", msg)
	}
	// Must specifically be the schema validator, not a generic 500 or a
	// gateway-binder error. The schema-validation message includes the
	// "buyer_input" token.
	if !strings.Contains(msg, "buyer_input") {
		t.Fatalf("rejection should come from schema validator (mentioning buyer_input), got: %s", msg)
	}
}

// TestTradingReleaseRejectionDoesNotMutateState verifies that a release
// attempt rejected by the chief verifier (or by the empty-transfer_id
// guard) leaves the order's status and transfer_id untouched and does
// not emit a released event. This is the strongest HTTP-layer check
// that the verify gate sits BEFORE the atomic TransitionOrderStatus
// write — a rejected verify must not advance state.
func TestTradingReleaseRejectionDoesNotMutateState(t *testing.T) {
	testutil.WaitForAPI(t)

	sellerEmail := fmt.Sprintf("trade_v2_reject_seller_%d@test.com", time.Now().UnixNano())
	buyerEmail := fmt.Sprintf("trade_v2_reject_buyer_%d@test.com", time.Now().UnixNano())
	sellerReg := testutil.RegisterAgent(t, sellerEmail, "RejectSeller", "v2 reject seller")
	buyerReg := testutil.RegisterAgent(t, buyerEmail, "RejectBuyer", "v2 reject buyer")
	t.Cleanup(func() { testutil.CleanupTestEmails(t, sellerEmail, buyerEmail) })
	sellerToken := sellerReg["token"].(string)
	buyerToken := buyerReg["token"].(string)
	buyerAgentIDStr := buyerReg["agent_id"].(string)

	serviceID := publishTradingServiceForE2E(t, sellerToken, "Release rejection seed", "")

	createResp := testutil.DoPost(t, "/api/v1/trading/orders",
		map[string]interface{}{
			"service_id":  serviceID,
			"buyer_input": `{"note":"reject test"}`,
		},
		buyerToken,
	)
	if code, _ := createResp["code"].(float64); code != 0 {
		t.Fatalf("create order failed: %#v", createResp)
	}
	orderData, _ := createResp["data"].(map[string]interface{})
	orderIDStr, _ := orderData["order_id"].(string)
	if orderIDStr == "" {
		t.Fatalf("create order response missing order_id: %#v", createResp)
	}

	// Advance to delivered.
	deliverResp := testutil.DoPost(t,
		fmt.Sprintf("/api/v1/trading/orders/%s/deliver", orderIDStr),
		map[string]interface{}{
			"delivery_payload": "e2e delivery",
		},
		sellerToken,
	)
	if code, _ := deliverResp["code"].(float64); code != 0 {
		t.Fatalf("deliver failed: %#v", deliverResp)
	}

	// First rejection path: empty transfer_id (bind or RPC guard rejects it).
	empty := testutil.DoPost(t,
		fmt.Sprintf("/api/v1/trading/orders/%s/release", orderIDStr),
		map[string]interface{}{
			"buyer_agent_id": buyerAgentIDStr,
		},
		buyerToken,
	)
	if code, _ := empty["code"].(float64); code == 0 {
		t.Fatalf("empty transfer_id release must be rejected: %#v", empty)
	}

	// Second rejection path: synthesized transfer_id that chief cannot find.
	missing := testutil.DoPost(t,
		fmt.Sprintf("/api/v1/trading/orders/%s/release", orderIDStr),
		map[string]interface{}{
			"buyer_agent_id": buyerAgentIDStr,
			"transfer_id":    "t_e2e_missing",
		},
		buyerToken,
	)
	if code, _ := missing["code"].(float64); code == 0 {
		t.Fatalf("missing transfer release must be rejected: %#v", missing)
	}

	// Order must remain delivered (status=2) and have no stored transfer_id.
	getResp := testutil.DoGet(t, "/api/v1/trading/orders/"+orderIDStr, buyerToken)
	getData, _ := getResp["data"].(map[string]interface{})
	order, _ := getData["order"].(map[string]interface{})
	if got, _ := order["status"].(float64); got != 2 {
		t.Fatalf("order must remain delivered (status=2) after rejected releases, got %v", order["status"])
	}
	if got, _ := order["transfer_id"].(string); got != "" {
		t.Fatalf("transfer_id must remain empty after rejected releases; got %q", got)
	}

	// No released event must have been emitted.
	events, _ := getData["events"].([]interface{})
	for _, raw := range events {
		ev, _ := raw.(map[string]interface{})
		if t2, _ := ev["event_type"].(string); t2 == "released" {
			t.Fatalf("rejected releases must not emit a released event; events=%v", events)
		}
	}
}
