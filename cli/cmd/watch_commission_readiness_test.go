package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/dispatch"
)

func TestCommissionPaymentReusesPreAcceptanceCheckAcrossVersions(t *testing.T) {
	order := paidCommissionTestOrder()
	order.State, order.Version = "awaiting_seller", 1
	var w *accountWatch
	var calls, accepts int
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/orders/901":
			materialTestResponse(out, map[string]any{"order": order})
		case "/api/v2/console/trade/orders/901":
			materialTestResponse(out, map[string]any{"order_id": "901", "role": "seller", "version": order.Version, "state": order.State, "counterparty": map[string]string{"agent_id": "41"}, "files": map[string]any{"input": []any{}}})
		case "/api/v1/orders/901/accept":
			if calls != 1 || order.State != "awaiting_seller" {
				t.Error("acceptance did not follow one actual inspection")
			}
			persisted, err := dispatch.ReadJournalStatus(*w.binding)
			if err != nil || len(persisted) != 1 || persisted[0].CommissionReadiness == nil || persisted[0].CommissionReadiness.Result.OrderVersion != 1 || persisted[0].CommissionReadiness.InputFingerprint == "" {
				t.Errorf("acceptance preceded durable readiness: %+v %v", persisted, err)
			}
			accepts++
			order.State, order.Version = "pending_payment", 2
			materialTestResponse(out, map[string]any{"order": order})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(out, r)
		}
	}))
	defer server.Close()
	w = newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, w)
	w.runAgent = func(_ context.Context, request dispatch.Request) (dispatch.Result, error) {
		calls++
		result := dispatch.CommissionIntakeDecision{Version: 1, RequestID: request.ID, OrderID: "901", OrderVersion: 1, Outcome: "ready", Summary: "Read the original inputs", InspectedFiles: []string{}}
		raw, _ := json.Marshal(result)
		return dispatch.Result{Text: string(raw)}, nil
	}
	for _, version := range []int64{1, 2, 3} {
		if version == 3 {
			order.State, order.Version = "in_progress", 3
		}
		job := claimCommissionIntakeVersion(t, w, version)
		if err := w.dispatchCommissionIntake(context.Background(), job); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 || accepts != 1 {
		t.Fatalf("inspection or acceptance repeated: calls=%d accepts=%d", calls, accepts)
	}
	fulfillment, ok, err := w.journal.NextKind("commission_fulfillment")
	if err != nil || !ok {
		t.Fatalf("paid fulfillment not queued: %v %v", ok, err)
	}
	var payload dispatch.CommissionFulfillmentJob
	if err := json.Unmarshal(fulfillment.Data, &payload); err != nil || payload.OrderVersion != 3 || payload.IntakeResult.OrderVersion != 1 || payload.IntakeRequestID != payload.IntakeResult.RequestID || payload.InputFingerprint == "" {
		t.Fatalf("historical inspection version was lost: %+v %v", payload, err)
	}
	if _, err := dispatch.ReadJournalStatus(*w.binding); err != nil {
		t.Fatalf("cross-version evidence cannot survive restart: %v", err)
	}
}

func TestCommissionPaidChangedInputsBlockWithoutCallingAgent(t *testing.T) {
	for _, change := range []string{"buyer_input", "contract", "missing_evidence"} {
		t.Run(change, func(t *testing.T) {
			order := paidCommissionTestOrder()
			server := commissionFulfillmentServer(t, func() commissionIntakeOrder { return order })
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			installCommissionIntakeRules(t, w)
			if change != "missing_evidence" {
				prior := claimCommissionIntakeVersion(t, w, 1)
				result := dispatch.CommissionIntakeDecision{Version: 1, RequestID: prior.ID, OrderID: "901", OrderVersion: 1, Outcome: "ready", Summary: "Checked before accepting", InspectedFiles: []string{}}
				fingerprint, err := commissionInputFingerprint(order, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := w.journal.SaveCommissionReadiness(prior.ID, dispatch.CommissionReadiness{Result: result, InputFingerprint: fingerprint}); err != nil {
					t.Fatal(err)
				}
				if err := w.journal.CompleteCommissionIntake(prior.ID, result, "original"); err != nil {
					t.Fatal(err)
				}
			}
			if change == "buyer_input" {
				order.BuyerInput += " changed"
			}
			if change == "contract" {
				order.Contract = json.RawMessage(`{"fulfillment_skill":"input-review","requires_materials":false,"delivery_spec_text":"changed scope"}`)
			}
			w.runAgent = func(context.Context, dispatch.Request) (dispatch.Result, error) {
				t.Fatal("paid order repeated model inspection")
				return dispatch.Result{}, nil
			}
			job := claimCommissionIntakeVersion(t, w, 3)
			if err := w.dispatchCommissionIntake(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			if w.journal.HasCommissionFulfillment("901") {
				t.Fatal("changed or uninspected inputs authorized fulfillment")
			}
			for _, saved := range w.journal.Snapshot() {
				if saved.ID == job.ID && saved.Status != "needs_user" {
					t.Fatalf("unsafe order not blocked: %+v", saved)
				}
			}
		})
	}
}

func TestCommissionInputFingerprintUsesContentNotLifecycleOrLocalPaths(t *testing.T) {
	order := testCommissionOrder()
	order.Contract = json.RawMessage(`{"requires_materials":true,"large_id":9007199254740993,"fulfillment_skill":"input-review"}`)
	files := []commissionLocalFile{{LogicalPath: "inputs/b", Path: "/private/first", SHA256: strings.Repeat("a", 64), ByteSize: 12}, {LogicalPath: "inputs/a", Path: "/private/second", SHA256: strings.Repeat("b", 64), ByteSize: 15}}
	want, err := commissionInputFingerprint(order, files)
	if err != nil {
		t.Fatal(err)
	}
	order.State, order.Version, order.SnapshotID = "in_progress", 8, 999
	order.Contract = json.RawMessage(`{ "fulfillment_skill":"input-review", "large_id":9007199254740993, "requires_materials":true }`)
	files[0], files[1] = files[1], files[0]
	files[0].Path = "/private/new-download"
	got, err := commissionInputFingerprint(order, files)
	if err != nil || got != want {
		t.Fatalf("equivalent immutable inputs changed fingerprint: %s %s %v", got, want, err)
	}
	for _, change := range []string{"bytes", "size", "logical_path", "large_number"} {
		t.Run(change, func(t *testing.T) {
			changedFiles := append([]commissionLocalFile{}, files...)
			changedOrder := order
			switch change {
			case "bytes":
				changedFiles[0].SHA256 = strings.Repeat("c", 64)
			case "size":
				changedFiles[0].ByteSize++
			case "logical_path":
				changedFiles[0].LogicalPath += ".different"
			case "large_number":
				changedOrder.Contract = json.RawMessage(`{"fulfillment_skill":"input-review","large_id":9007199254740992,"requires_materials":true}`)
			}
			value, err := commissionInputFingerprint(changedOrder, changedFiles)
			if err != nil || value == want {
				t.Fatal(fmt.Sprintf("%s did not invalidate evidence: %v", change, err))
			}
		})
	}
}
