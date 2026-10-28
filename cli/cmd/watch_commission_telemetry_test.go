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
