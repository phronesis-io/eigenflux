package dispatch

import (
	"cli.eigenflux.ai/internal/maintenance"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSafeDiagnosticsAreSpecificAndNeverRaw(t *testing.T) {
	rpc := -32000
	exit := 7
	for _, tc := range []struct {
		name  string
		cause error
		code  string
	}{
		{"exact extension", errors.New("acp_extension_required"), "acp_extension_required"},
		{"wrapped WorkBuddy", fmt.Errorf("%w: %w", ErrMainNoticeNotSubmitted, errors.New("workbuddy_host_unavailable")), "workbuddy_host_unavailable"},
		{"unsupported", ErrMainNoticeUnsupported, "main_notice_transport_unverified"},
		{"deadline", context.DeadlineExceeded, "timeout"},
		{"unknown stderr", errors.New("private token /absolute/path"), "unknown_failure"},
		{"injected suffix", errors.New("workbuddy_host_unavailable private-token"), "unknown_failure"},
		{"RPC WorkBuddy", &bridgeDiagnosticError{cause: &DiagnosticError{Code: "acp_rpc_error", RPCCode: &rpc}, message: "workbuddy_existing_conversation_not_supported"}, "workbuddy_existing_conversation_not_supported"},
		{"exit", &DiagnosticError{Code: "runner_exit_failed", ExitCode: &exit}, "runner_exit_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SafeDiagnostic(tc.cause)
			if got.Code != tc.code || maintenance.ValidateCommissionBridgeError(got) != nil {
				t.Fatalf("%+v", got)
			}
			raw, _ := json.Marshal(got)
			if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "/absolute") {
				t.Fatal(string(raw))
			}
		})
	}
	got := SafeDiagnostic(&bridgeDiagnosticError{cause: &DiagnosticError{Code: "acp_rpc_error", RPCCode: &rpc}, message: "workbuddy_agent_rejected"})
	if got.RPCCode == nil || *got.RPCCode != rpc {
		t.Fatal(got)
	}
}

func TestCommissionDiagnosticJournalRestartAndNoBusinessReplay(t *testing.T) {
	j := commissionJournal(t)
	mustAddCommission(t, j, commissionNotification(901, 3, "seller", true))
	job, _, _ := j.NextKind("commission_order")
	if err := j.SetCommissionExecution(job.ID, "901", 3, 0); err != nil {
		t.Fatal(err)
	}
	if err := j.SaveCommissionDiagnostic(job.ID, &maintenance.CommissionBridgeError{Code: "workbuddy_agent_rejected", Message: "workbuddy_agent_rejected"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := j.Update(job.ID, "needs_user", "commission_skill_read_scope_invalid", "", ""); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, j.binding)
	before := j.Snapshot()[0]
	snapshots := j.CommissionDiagnosticSnapshots(32)
	if len(snapshots) != 1 {
		t.Fatal(snapshots)
	}
	c := snapshots[0].Observation
	if c.LocalCode != "commission_skill_read_scope_invalid" || c.CheckReceipt.Status != "missing" || c.BridgeError.Code != "workbuddy_agent_rejected" {
		t.Fatal(c)
	}
	if err := j.AcknowledgeCommissionDiagnosticSnapshot(job.ID, snapshots[0].Key); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, j.binding)
	if len(j.CommissionDiagnosticSnapshots(32)) != 0 {
		t.Fatal("snapshot repeated after restart")
	}
	after := j.Snapshot()[0]
	if before.Status != after.Status || before.Code != after.Code || before.CommissionExecution.Reported != after.CommissionExecution.Reported {
		t.Fatal("diagnostics changed business execution")
	}
	if _, ok, err := j.NextKind("commission_order"); err != nil || ok {
		t.Fatal("diagnostic replay claimed blocked work", err)
	}
}

func TestCommissionDiagnosticReceiptPreservesEarlierAndFulfillmentEvidence(t *testing.T) {
	j := commissionJournal(t)
	source, evidence := acceptedReadiness(t, j, 901)
	mustAddCommission(t, j, commissionNotification(901, 3, "seller", true))
	paid, _, _ := j.NextKind("commission_order")
	if err := j.SetCommissionExecution(paid.ID, "901", 3, 0); err != nil {
		t.Fatal(err)
	}
	c, _ := j.CommissionDiagnosticObservation(paid.ID)
	if c.CheckReceipt.Status != "present" || c.CheckReceipt.SourceJobID != source.ID || c.CheckReceipt.CheckedOrderVersion != 1 || c.CheckReceipt.InspectedFileCount == nil || *c.CheckReceipt.InspectedFileCount != 0 || c.CheckReceipt.InputFingerprint != evidence.InputFingerprint {
		t.Fatalf("%+v", c.CheckReceipt)
	}
	if err := j.QueueCommissionFromReadiness(paid.ID, source.ID, "901", 3); err != nil {
		t.Fatal(err)
	}
	fulfillment, ok, _ := j.NextKind("commission_fulfillment")
	if !ok {
		t.Fatal("no fulfillment")
	}
	if err := j.Update(fulfillment.ID, "failed", "commission_fulfillment_unconfirmed", "", ""); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, j.binding)
	for _, snapshot := range j.CommissionDiagnosticSnapshots(32) {
		if snapshot.Observation.JobID == fulfillment.ID {
			r := snapshot.Observation.CheckReceipt
			if r.Status != "present" || r.SourceJobID != source.ID || r.InputFingerprint != evidence.InputFingerprint {
				t.Fatalf("%+v", r)
			}
			raw, _ := json.Marshal(snapshot)
			if strings.Contains(string(raw), evidence.Result.Summary) || strings.Contains(string(raw), "inputs/") {
				t.Fatal("receipt leaked private summary/paths")
			}
			return
		}
	}
	t.Fatal("missing fulfillment snapshot")
}

func TestRunnerDiagnosticsRetainActualExitAndWorkBuddyRPC(t *testing.T) {
	binding := fixtureBinding(t, "command", "exit")
	_, err := (Runner{Binding: binding}).Run(context.Background(), Request{Prompt: "private prompt"})
	got := SafeDiagnostic(err)
	if got.ExitCode == nil || *got.ExitCode != 7 || got.Code != "runner_exit_failed" {
		t.Fatalf("%+v / %v", got, err)
	}
	for _, scenario := range []string{"acp-stderr-diagnostic", "acp-rpc-diagnostic"} {
		binding = fixtureBinding(t, "acp", scenario)
		binding.Env["EF_ACP_DIAGNOSTIC"] = "workbuddy_existing_conversation_not_supported"
		_, err = (Runner{Binding: binding}).Run(context.Background(), Request{Prompt: "private prompt"})
		got = SafeDiagnostic(err)
		if got.Code != "workbuddy_existing_conversation_not_supported" {
			t.Fatalf("%+v / %v", got, err)
		}
		if scenario == "acp-rpc-diagnostic" && (got.RPCCode == nil || *got.RPCCode != -32000) {
			t.Fatalf("lost actual RPC code: %+v", got)
		}
		if scenario == "acp-stderr-diagnostic" && got.ExitCode == nil {
			t.Fatal("lost actual ACP process exit")
		}
	}
}

func TestDiagnosticSnapshotStaleAcknowledgementCannotHideNewEvidence(t *testing.T) {
	j := commissionJournal(t)
	mustAddCommission(t, j, commissionNotification(901, 3, "seller", true))
	job, _, _ := j.NextKind("commission_order")
	if err := j.Update(job.ID, "failed", "commission_intake_unconfirmed", "", ""); err != nil {
		t.Fatal(err)
	}
	old := j.CommissionDiagnosticSnapshots(32)[0]
	if err := j.SaveCommissionDiagnostic(job.ID, &maintenance.CommissionBridgeError{Code: "workbuddy_agent_rejected", Message: "workbuddy_agent_rejected"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := j.AcknowledgeCommissionDiagnosticSnapshot(job.ID, old.Key); err != nil {
		t.Fatal(err)
	}
	current := j.CommissionDiagnosticSnapshots(32)
	if len(current) != 1 || current[0].Key == old.Key {
		t.Fatal("stale ack hid newer evidence")
	}
	if err := j.Retry(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := j.AcknowledgeCommissionDiagnosticSnapshot(job.ID, current[0].Key); err != nil {
		t.Fatal(err)
	}
	if value := j.Snapshot()[0]; value.Code != "operator_retry" || value.Status != "pending" || value.CommissionDiagnostic.SnapshotReported != "" {
		t.Fatalf("stale ack changed retried job: %+v", value)
	}
}

func TestMainNoticeBridgeDiagnosticSurvivesRestartWithoutReplay(t *testing.T) {
	binding := validTestBinding(t)
	q, err := OpenMainNoticeQueue(binding)
	if err != nil {
		t.Fatal(err)
	}
	item := MainNotice{ID: "11", OrderID: "901", Version: 3, Role: "seller", Event: "order.state.changed.v1", State: "awaiting_seller"}
	if err = q.Add(item); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := q.Next(); err != nil || !ok {
		t.Fatal(err)
	}
	if err = q.SaveBridgeDiagnostic("11", &maintenance.CommissionBridgeError{Code: "acp_extension_required", Message: "acp_extension_required"}); err != nil {
		t.Fatal(err)
	}
	if err = q.Finish("11", "skipped", "main_notice_unconfirmed"); err != nil {
		t.Fatal(err)
	}
	q, err = OpenMainNoticeQueue(binding)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := q.Lookup("11")
	if !ok || got.BridgeError == nil || got.BridgeError.Code != "acp_extension_required" {
		t.Fatal(got)
	}
	got.BridgeError.Code = "private injected value"
	again, _ := q.Lookup("11")
	if again.BridgeError.Code != "acp_extension_required" {
		t.Fatal("diagnostic pointer escaped")
	}
	if _, ok, err := q.Next(); err != nil || ok {
		t.Fatal("diagnostic persistence replayed notice", err)
	}
}
