package dispatch

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommissionReadinessPersistsBeforeAcceptanceAndQueuesPaidVersion(t *testing.T) {
	j := commissionJournal(t)
	mustAddCommission(t, j, commissionNotification(901, 1, "seller", false))
	source, ok, err := j.NextKind("commission_order")
	if err != nil || !ok {
		t.Fatal("missing inspection")
	}
	value := CommissionReadiness{Result: CommissionIntakeDecision{Version: 1, RequestID: source.ID, OrderID: "901", OrderVersion: 1, Outcome: "ready", Summary: "Actual original check", InspectedFiles: []string{"inputs/request.txt"}}, InputFingerprint: strings.Repeat("a", 64)}
	if err := j.SaveCommissionReadiness(source.ID, value); err != nil {
		t.Fatal(err)
	}
	// Simulate a restart after evidence was committed but before acceptance could
	// be recorded. The actual check remains usable once payment is authoritative.
	j = mustJournal(t, j.binding)
	if got := j.Snapshot()[0]; got.Status != "unknown" || got.CommissionReadiness == nil || got.CommissionReadiness.Result.OrderVersion != 1 {
		t.Fatalf("check lost at interruption: %+v", got)
	}
	snapshot := j.Snapshot()
	snapshot[0].CommissionReadiness.Result.InspectedFiles[0] = "changed-by-reader"
	if j.Snapshot()[0].CommissionReadiness.Result.InspectedFiles[0] != "inputs/request.txt" {
		t.Fatal("readiness aliases mutable reader memory")
	}
	mustAddCommission(t, j, commissionNotification(901, 3, "seller", false))
	paid, ok, err := j.NextKind("commission_order")
	if err != nil || !ok {
		t.Fatal("missing paid event")
	}
	if err := j.QueueCommissionFromReadiness(paid.ID, source.ID, "901", 3); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, j.binding)
	fulfillment, ok, err := j.NextKind("commission_fulfillment")
	if err != nil || !ok {
		t.Fatal("fulfillment did not survive restart")
	}
	var queued CommissionFulfillmentJob
	if err := json.Unmarshal(fulfillment.Data, &queued); err != nil || queued.OrderVersion != 3 || queued.IntakeResult.OrderVersion != 1 || queued.IntakeRequestID != source.ID || queued.InputFingerprint != value.InputFingerprint {
		t.Fatalf("forged or lost readiness: %+v %v", queued, err)
	}
}

func TestCommissionReadinessRejectsUnverifiedOrMismatchedEvidence(t *testing.T) {
	for _, scenario := range []string{"not_ready", "invalid_fingerprint", "wrong_request", "wrong_order", "future_check", "buyer_event", "missing_source"} {
		t.Run(scenario, func(t *testing.T) {
			j := commissionJournal(t)
			mustAddCommission(t, j, commissionNotification(901, 1, "seller", false))
			source, _, _ := j.NextKind("commission_order")
			value := CommissionReadiness{Result: CommissionIntakeDecision{Version: 1, RequestID: source.ID, OrderID: "901", OrderVersion: 1, Outcome: "ready", Summary: "Actual check", InspectedFiles: []string{}}, InputFingerprint: strings.Repeat("a", 64)}
			switch scenario {
			case "not_ready":
				value.Result.Outcome = "needs_input"
			case "invalid_fingerprint":
				value.InputFingerprint = "model said ready"
			case "wrong_request":
				value.Result.RequestID = "other"
			case "wrong_order":
				value.Result.OrderID = "902"
			case "future_check":
				value.Result.OrderVersion = 4
			}
			err := j.SaveCommissionReadiness(source.ID, value)
			if scenario == "not_ready" || scenario == "invalid_fingerprint" || scenario == "wrong_request" || scenario == "wrong_order" {
				if err == nil {
					t.Fatal("invalid evidence persisted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			role := "seller"
			if scenario == "buyer_event" {
				role = "buyer"
			}
			mustAddCommission(t, j, commissionNotification(901, 3, role, false))
			paid, _, _ := j.NextKind("commission_order")
			sourceID := source.ID
			if scenario == "missing_source" {
				sourceID = "absent"
			}
			if err := j.QueueCommissionFromReadiness(paid.ID, sourceID, "901", 3); err == nil {
				t.Fatal("unverified source or role authorized fulfillment")
			}
		})
	}
}
