package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/maintenance"
)

func commissionObservations(t *testing.T, w *accountWatch) []maintenance.Event {
	t.Helper()
	state, err := maintenance.Snapshot(w.maintenanceScope())
	if err != nil {
		t.Fatal(err)
	}
	var events []maintenance.Event
	for _, event := range state.Events {
		if event.Commission == nil {
			continue
		}
		if err := maintenance.Validate(event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func TestCommissionTelemetryRecordsPreflightFailureWithoutExecutionContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
		http.Error(out, "private upstream error", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, w)
	job := claimCommissionIntake(t, w)
	if err := w.dispatchCommissionIntake(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if current := w.commissionDeliveryJob(job); current.Status != "failed" || current.CommissionExecution != nil {
		t.Fatal(current)
	}
	events := commissionObservations(t, w)
	if len(events) != 3 || events[0].Result != "started" || events[1].Commission.Stage != "preflight" || events[1].Commission.HTTPStatus != 503 || events[2].Result != "failed" || events[2].Commission.OrderID != "901" || events[2].Commission.SourceNotificationID != "81" {
		t.Fatal(events)
	}
}

func TestCommissionAgentTelemetryPreservesSafePermissionReceiptOnly(t *testing.T) {
	w := newCommissionWatch(t, "http://127.0.0.1:1", "commission_order")
	job := claimCommissionIntake(t, w)
	w.runAgent = func(context.Context, dispatch.Request) (dispatch.Result, error) {
		return dispatch.Result{Text: "private model answer", SessionID: "private-session", RunID: "private-turn"}, &dispatch.DiagnosticError{Cause: dispatch.ErrNeedsUser, Code: "permission_required", Operation: "read"}
	}
	_, err := w.runCommissionAgent(context.Background(), job, dispatch.Request{Prompt: "private prompt"})
	if !errors.Is(err, dispatch.ErrNeedsUser) {
		t.Fatal(err)
	}
	events := commissionObservations(t, w)
	if len(events) != 2 {
		t.Fatal(events)
	}
	last := events[1]
	if last.Result != "blocked" || last.ErrorCode != "permission_required" || last.Commission.Operation != "read" || last.Commission.HostSessionIDHash != commissionDiagnosticHash("private-session") || last.Commission.HostRunIDHash != commissionDiagnosticHash("private-turn") {
		t.Fatal(last)
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), "private") {
		t.Fatal("raw prompt, result or host receipt leaked")
	}
	if current := w.commissionDeliveryJob(job); current.Status != "running" {
		t.Fatal("observation changed job", current.Status)
	}
}

func TestCommissionMainNoticeTelemetryRecordsReceiptWithoutModelText(t *testing.T) {
	w := newCommissionWatch(t, "http://127.0.0.1:1", "commission_order")
	installCommissionIntakeRules(t, w)
	w.runMainNotice = func(context.Context, string) (dispatch.Result, error) {
		return dispatch.Result{Text: "private displayed result", SessionID: "owner-session", RunID: "owner-run"}, nil
	}
	status, _ := w.dispatchCommissionMainNotice(context.Background(), dispatch.MainNotice{ID: "91", OrderID: "901", Version: 3, Role: "buyer", State: "pending_payment", Attempts: 1})
	if status != "delivered" {
		t.Fatal(status)
	}
	events := commissionObservations(t, w)
	if len(events) != 4 {
		t.Fatal(events)
	}
	if events[2].Commission.HostRunIDHash != commissionDiagnosticHash("owner-run") || events[3].Result != "succeeded" {
		t.Fatal(events)
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "owner-session") || strings.Contains(string(raw), "owner-run") {
		t.Fatal("host content leaked")
	}
}

func TestCommissionUploadTelemetryKeepsFailingSubstageAndHTTPStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
		http.Error(out, "sensitive provider body", http.StatusBadGateway)
	}))
	defer server.Close()
	api := client.New(server.URL, "private-token", "1.0.0", client.Meta{})
	calls := 0
	err := uploadCommissionDeliveryFile(context.Background(), api, "/orders/901", "key", materialFile{LogicalPath: "outputs/private.txt"}, func(stage string, _ time.Time, cause error) {
		calls++
		var failure *client.APIError
		if stage != "upload_grant" || !errors.As(cause, &failure) || failure.StatusCode != http.StatusBadGateway {
			t.Fatal(stage, cause)
		}
	})
	if err == nil || calls != 1 {
		t.Fatal(err, calls)
	}
}

func TestCommissionTelemetryFailureDoesNotRepeatOrFailAgentCall(t *testing.T) {
	w := newCommissionWatch(t, "http://127.0.0.1:1", "commission_order")
	job := claimCommissionIntake(t, w)
	w.home = "relative-invalid-home"
	calls := 0
	w.runAgent = func(context.Context, dispatch.Request) (dispatch.Result, error) {
		calls++
		return dispatch.Result{Text: "ok"}, nil
	}
	if _, err := w.runCommissionAgent(context.Background(), job, dispatch.Request{}); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
}

func TestCommissionClarificationTelemetryUsesActualJobContract(t *testing.T) {
	w := newCommissionWatch(t, "http://127.0.0.1:1", "commission_order")
	data, _ := json.Marshal(dispatch.CommissionClarificationJob{Version: 1, OrderID: "901", OrderVersion: 3, BuyerID: "42", RequestID: strings.Repeat("a", 64), Round: 2})
	job := dispatch.Job{ID: strings.Repeat("b", 64), Kind: "commission_clarification", Data: data}
	c := commissionJobObservation(job)
	c.Stage = "agent"
	w.recordCommissionObservation(c, "started", "", time.Now(), dispatch.Result{}, nil)
	events := commissionObservations(t, w)
	if len(events) != 1 || events[0].Commission.InputRequestIDHash != commissionDiagnosticHash(strings.Repeat("a", 64)) || events[0].Commission.InputRound != 2 || events[0].Commission.Role != "buyer" {
		t.Fatal(events)
	}
}

func TestCommissionTelemetryPhysicalReceiptReplayIsIdempotent(t *testing.T) {
	w := newCommissionWatch(t, "http://127.0.0.1:1", "commission_order")
	job := claimCommissionIntake(t, w)
	c := commissionJobObservation(job)
	c.Stage = "agent"
	started := time.Now()
	w.recordCommissionObservation(c, "started", "", started, dispatch.Result{}, nil)
	w.recordCommissionObservation(c, "succeeded", "", started, dispatch.Result{}, nil)
	w.recordCommissionObservation(c, "succeeded", "", started, dispatch.Result{}, nil)
	events := commissionObservations(t, w)
	if len(events) != 2 || events[0].AttemptID != events[1].AttemptID {
		t.Fatal(events)
	}
	// Another actual host call is a new receipt even when the Order is unchanged.
	w.recordCommissionObservation(c, "succeeded", "", started.Add(time.Nanosecond), dispatch.Result{}, nil)
	if len(commissionObservations(t, w)) != 3 {
		t.Fatal("physical attempts collapsed")
	}
}

func TestCommissionRetryDoesNotReusePriorBridgeFailure(t *testing.T) {
	w := newCommissionWatch(t, "http://127.0.0.1:1", "commission_order")
	job := claimCommissionIntake(t, w)
	calls := 0
	w.runAgent = func(context.Context, dispatch.Request) (dispatch.Result, error) {
		calls++
		if calls == 1 {
			return dispatch.Result{}, &dispatch.DiagnosticError{Cause: dispatch.ErrNeedsUser, Code: "permission_required", Operation: "read"}
		}
		return dispatch.Result{Text: "verified local result"}, nil
	}
	if _, err := w.runCommissionAgent(context.Background(), job, dispatch.Request{}); err == nil {
		t.Fatal("missing first failure")
	}
	if err := w.journal.Update(job.ID, "failed", "commission_intake_unconfirmed", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := w.journal.Retry(job.ID); err != nil {
		t.Fatal(err)
	}
	retried, ok, err := w.journal.NextKind("commission_order")
	if err != nil || !ok {
		t.Fatal(err)
	}
	finish := w.observeCommissionJob(retried)
	if _, err := w.runCommissionAgent(context.Background(), retried, dispatch.Request{}); err != nil {
		t.Fatal(err)
	}
	if err := w.finishDispatch(retried, "completed", "commission_order_not_actionable", "", ""); err != nil {
		t.Fatal(err)
	}
	finish()
	for _, event := range commissionObservations(t, w) {
		if (event.Result == "started" || event.Result == "succeeded") && event.Commission.BridgeError != nil {
			t.Fatalf("new attempt carried old error: %+v", event.Commission.BridgeError)
		}
	}
	if calls != 2 {
		t.Fatal("diagnostics reran the model", calls)
	}
}

func TestCommissionStartupBackfillIsDurableObservationOnly(t *testing.T) {
	w := newCommissionWatch(t, "http://127.0.0.1:1", "commission_order")
	job := claimCommissionIntake(t, w)
	if err := w.journal.SetCommissionExecution(job.ID, "901", 3, 0); err != nil {
		t.Fatal(err)
	}
	if err := w.journal.Update(job.ID, "needs_user", "commission_skill_read_scope_invalid", "", ""); err != nil {
		t.Fatal(err)
	}
	w.runAgent = func(context.Context, dispatch.Request) (dispatch.Result, error) {
		t.Fatal("snapshot reran model")
		return dispatch.Result{}, nil
	}
	before := w.commissionDeliveryJob(job)
	w.backfillCommissionDiagnostics()
	events := commissionObservations(t, w)
	if len(events) != 1 || events[0].Result != "observed" || events[0].Commission.Stage != "diagnostic_snapshot" || events[0].Commission.LocalCode != "commission_skill_read_scope_invalid" || events[0].Commission.BridgeError.Code != "unknown_failure" || events[0].Commission.CheckReceipt.Status != "missing" || events[0].Commission.Attempt != 0 || events[0].DurationMS != 0 {
		t.Fatalf("%+v", events)
	}
	recovered, err := dispatch.OpenJournal(*w.binding)
	if err != nil {
		t.Fatal(err)
	}
	w.journal = recovered
	w.backfillCommissionDiagnostics()
	if len(commissionObservations(t, w)) != 1 {
		t.Fatal("startup repeated diagnostic snapshot")
	}
	after := w.commissionDeliveryJob(job)
	if before.Code != after.Code || before.Status != after.Status || before.CommissionExecution.Reported != after.CommissionExecution.Reported {
		t.Fatal("diagnostics changed business job")
	}
}

func TestCommissionDiagnosticSnapshotKeepsCapturedContent(t *testing.T) {
	w := newCommissionWatch(t, "http://127.0.0.1:1", "commission_order")
	job := claimCommissionIntake(t, w)
	if err := w.journal.Update(job.ID, "failed", "commission_intake_unconfirmed", "", ""); err != nil {
		t.Fatal(err)
	}
	captured := w.journal.CommissionDiagnosticSnapshots(32)[0]
	if err := w.journal.SaveCommissionDiagnostic(job.ID, &maintenance.CommissionBridgeError{Code: "workbuddy_agent_rejected", Message: "workbuddy_agent_rejected"}, nil); err != nil {
		t.Fatal(err)
	}
	if !w.recordCommissionObservation(captured.Observation, "observed", "execution_failed", time.Time{}, dispatch.Result{}, nil, captured.Key) {
		t.Fatal("failed to record snapshot")
	}
	events := commissionObservations(t, w)
	if len(events) != 1 || events[0].Commission.BridgeError.Code != "unknown_failure" || events[0].AttemptID != captured.Key {
		t.Fatal("snapshot was rehydrated under an old key", events)
	}
	if err := w.journal.AcknowledgeCommissionDiagnosticSnapshot(job.ID, captured.Key); err != nil {
		t.Fatal(err)
	}
	if len(w.journal.CommissionDiagnosticSnapshots(32)) != 1 {
		t.Fatal("stale snapshot hid newly saved diagnosis")
	}
}

func TestMainNoticeRetryPreflightDoesNotReuseBridgeFailure(t *testing.T) {
	w := newCommissionWatch(t, "http://127.0.0.1:1", "commission_order")
	w.runMainNotice = func(context.Context, string) (dispatch.Result, error) {
		t.Fatal("preflight must not run bridge")
		return dispatch.Result{}, nil
	}
	item := dispatch.MainNotice{ID: "91", OrderID: "901", Version: 3, Role: "buyer", State: "pending_payment", Attempts: 2, BridgeError: &maintenance.CommissionBridgeError{Code: "workbuddy_agent_rejected", Message: "workbuddy_agent_rejected"}}
	_, _ = w.dispatchCommissionMainNotice(context.Background(), item)
	for _, event := range commissionObservations(t, w) {
		if event.Commission.BridgeError != nil {
			t.Fatalf("new preflight reused old bridge failure: %+v", event.Commission.BridgeError)
		}
	}
}
