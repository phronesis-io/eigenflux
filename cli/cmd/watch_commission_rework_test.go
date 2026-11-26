package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/dispatch"
)

func testReworkOrder() commissionIntakeOrder {
	order := paidCommissionTestOrder()
	order.Version = 5
	order.SnapshotID = 9007199254740995
	order.ExecutionRevision = 8
	order.DeliveryDueAt = time.Now().Add(time.Hour).UnixMilli()
	order.DeliveryReview = &dispatch.CommissionDeliveryReview{SubmissionNumber: 1, Status: "changes_requested", Reason: "The contracted conclusion is missing.", SnapshotID: json.Number("9007199254740993"), ReworkDueAt: order.DeliveryDueAt}
	return order
}

func TestCommissionReworkNotificationUsesFreshBoundSkillInvocation(t *testing.T) {
	order := testReworkOrder()
	server := commissionFulfillmentServer(t, func() commissionIntakeOrder { return order })
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, w)
	original := claimPaidCommission(t, w)
	if err := w.journal.Update(original.ID, "completed", "commission_delivery_confirmed", "original-session", ""); err != nil {
		t.Fatal(err)
	}
	intake := claimCommissionIntakeVersion(t, w, 5)
	if err := w.dispatchCommissionIntake(context.Background(), intake); err != nil {
		t.Fatal(err)
	}
	rework, ok, err := w.journal.NextKind("commission_fulfillment")
	if err != nil || !ok || rework.ID == original.ID {
		t.Fatalf("rework not queued: %+v %v jobs=%+v", rework, err, w.journal.Snapshot())
	}
	calls := 0
	w.runAgent = func(ctx context.Context, request dispatch.Request) (dispatch.Result, error) {
		calls++
		if request.SessionID != "" || request.ID == original.ID {
			t.Fatal("old session reused")
		}
		data := fulfillmentTestPayload(t, request)
		if data.Order.DeliveryReview == nil || data.Order.DeliveryReview.Reason != order.DeliveryReview.Reason || data.Order.DeliveryReview.ReworkDueAt != order.DeliveryDueAt || !strings.Contains(request.Prompt, `"submission_number":2`) || data.Intake.OrderVersion != 1 {
			t.Fatalf("incomplete rework context: %+v", data)
		}
		if err := os.WriteFile(filepath.Join(data.OutputDirectory, "revised.txt"), []byte("revised conclusion"), 0600); err != nil {
			t.Fatal(err)
		}
		result := dispatch.CommissionFulfillmentDecision{Version: 1, RequestID: request.ID, OrderID: "901", OrderVersion: 5, Outcome: "needs_user", Summary: "Revised locally; scoped upload permission required", SelfCheck: "Verified the revised conclusion", Artifacts: []dispatch.CommissionArtifact{{LogicalPath: "outputs/report.txt", RelativePath: "revised.txt"}}}
		raw, _ := json.Marshal(result)
		return dispatch.Result{Text: string(raw), SessionID: "new-rework-session"}, nil
	}
	if err := w.dispatchCommissionFulfillment(context.Background(), rework); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("model calls=%d", calls)
	}
	for _, job := range w.journal.Snapshot() {
		if job.ID == rework.ID && (job.CommissionFulfillment == nil || job.SessionID != "new-rework-session" || job.CommissionDirectory == "") {
			t.Fatalf("missing fresh evidence: %+v", job)
		}
		if job.ID == original.ID && (job.SessionID != "original-session" || job.Status != "completed") {
			t.Fatal("original job modified")
		}
	}
	// Old checkpoint/retry work cannot run against the new authoritative version.
	if err := w.journal.Retry(original.ID); err == nil {
		t.Fatal("completed first submission retried")
	}
	if calls != 1 {
		t.Fatal("obsolete first submission invoked the model")
	}
}

func TestCommissionReworkRegeneratesUploadsAndSubmitsSecondDelivery(t *testing.T) {
	order := paidCommissionTestOrder()
	server := commissionFulfillmentServer(t, func() commissionIntakeOrder { return order }, true)
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, w)
	var directories, requests []string
	w.runAgent = func(ctx context.Context, request dispatch.Request) (dispatch.Result, error) {
		data := fulfillmentTestPayload(t, request)
		if request.SessionID != "" {
			t.Fatal("model session reused")
		}
		directories = append(directories, data.OutputDirectory)
		requests = append(requests, request.ID)
		body := "Initial conclusion"
		if order.DeliveryReview != nil {
			body = "Revised conclusion satisfying the frozen contract"
			if !strings.Contains(request.Prompt, `"submission_number":2`) || data.Order.DeliveryReview.Reason != order.DeliveryReview.Reason {
				t.Fatal("missing defect feedback")
			}
		}
		if err := os.WriteFile(filepath.Join(data.OutputDirectory, "report.txt"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		decision := dispatch.CommissionFulfillmentDecision{Version: 1, RequestID: request.ID, OrderID: "901", OrderVersion: order.Version, Outcome: "artifacts_ready", Summary: "Contractual report generated", SelfCheck: "Read the report and verified the contractual conclusion", Artifacts: []dispatch.CommissionArtifact{{LogicalPath: "outputs/report.txt", RelativePath: "report.txt"}}}
		raw, _ := json.Marshal(decision)
		return dispatch.Result{Text: string(raw), SessionID: "session-" + request.ID}, nil
	}
	first := claimPaidCommission(t, w)
	if err := w.dispatchCommissionFulfillment(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	var retained dispatch.Job
	for _, job := range w.journal.Snapshot() {
		if job.ID == first.ID {
			retained = job
		}
	}
	if retained.Status != "completed" || retained.Code != "commission_delivery_confirmed" || retained.CommissionFulfillment == nil {
		t.Fatalf("first delivery not verified: %+v", retained)
	}
	order = testReworkOrder()
	intake := claimCommissionIntakeVersion(t, w, order.Version)
	if err := w.dispatchCommissionIntake(context.Background(), intake); err != nil {
		t.Fatal(err)
	}
	second, ok, err := w.journal.NextKind("commission_fulfillment")
	if err != nil || !ok {
		t.Fatalf("missing second submission: %v jobs=%+v", err, w.journal.Snapshot())
	}
	if err := w.dispatchCommissionFulfillment(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0] == requests[1] || directories[0] == directories[1] {
		t.Fatalf("execution identity reused: %v %v", requests, directories)
	}
	for _, job := range w.journal.Snapshot() {
		if job.Kind != "commission_fulfillment" {
			continue
		}
		if job.Status != "completed" || job.Code != "commission_delivery_confirmed" || job.CommissionFulfillment == nil || len(job.CommissionFulfillment.Artifacts) != 1 || job.CommissionFulfillment.Artifacts[0].LogicalPath != "outputs/report.txt" {
			t.Fatalf("submission not verified: %+v", job)
		}
		if job.ID == first.ID && job.CommissionFulfillment.Artifacts[0].SHA256 != retained.CommissionFulfillment.Artifacts[0].SHA256 {
			t.Fatal("first submission evidence replaced")
		}
		if job.ID == second.ID && job.CommissionFulfillment.Artifacts[0].SHA256 == retained.CommissionFulfillment.Artifacts[0].SHA256 {
			t.Fatal("old bytes reused as rework")
		}
	}
}

func TestCommissionReworkStopsAtReviewLimitAndDeadline(t *testing.T) {
	for _, scenario := range []string{"expired", "original_deadline", "second_submission", "manual_review", "reviewing", "failed", "passed", "closed", "missing_review_snapshot", "validating", "refund_pending"} {
		t.Run(scenario, func(t *testing.T) {
			order := testReworkOrder()
			switch scenario {
			case "expired":
				order.DeliveryReview.ReworkDueAt = time.Now().Add(-time.Second).UnixMilli()
			case "original_deadline":
				order.DeliveryDueAt = time.Now().Add(-time.Second).UnixMilli()
			case "second_submission":
				order.DeliveryReview.SubmissionNumber = 2
			case "missing_review_snapshot":
				order.DeliveryReview.SnapshotID = "0"
			case "validating", "refund_pending":
				order.State = scenario
			default:
				order.DeliveryReview.Status = scenario
			}
			if commissionFulfillmentWritable(order, time.Now()) {
				t.Fatal("forbidden state writable")
			}
			server := commissionFulfillmentServer(t, func() commissionIntakeOrder { return order })
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			installCommissionIntakeRules(t, w)
			queueCheckedPaidCommission(t, w)
			intake := claimCommissionIntakeVersion(t, w, 5)
			w.runAgent = func(context.Context, dispatch.Request) (dispatch.Result, error) {
				t.Fatal("ineligible rework invoked model")
				return dispatch.Result{}, nil
			}
			if err := w.dispatchCommissionIntake(context.Background(), intake); err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, job := range w.journal.Snapshot() {
				if job.Kind == "commission_fulfillment" {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("ineligible rework queued: %d", count)
			}
		})
	}
}

func TestCommissionDeliveryRecoveryMatchesFormalSubmission(t *testing.T) {
	order := testReworkOrder()
	order.State, order.Version = "validating", 6
	order.DeliveryReview.Status, order.DeliveryReview.SubmissionNumber = "reviewing", 2
	initial := dispatch.CommissionFulfillmentJob{Version: 1, OrderID: "901", OrderVersion: 3, IntakeRequestID: "inspection", IntakeResult: dispatch.CommissionIntakeDecision{Version: 1, RequestID: "inspection", OrderID: "901", OrderVersion: 1, Outcome: "ready", Summary: "Checked", InspectedFiles: []string{}}}
	raw, _ := json.Marshal(initial)
	if commissionDeliveryMatchesJob(order, dispatch.Job{Data: raw}, 3) {
		t.Fatal("first job confirmed the second submission")
	}
	initial.OrderVersion, initial.ExecutionRevision, initial.DeliveryReview = 5, 8, testReworkOrder().DeliveryReview
	raw, _ = json.Marshal(initial)
	if !commissionDeliveryMatchesJob(order, dispatch.Job{Data: raw}, 5) {
		t.Fatal("second job could not confirm its submission")
	}
}

func TestCommissionReworkFencesExplicitRetryOfOldDeliveryCheckpoint(t *testing.T) {
	order := testReworkOrder()
	server := commissionFulfillmentServer(t, func() commissionIntakeOrder { return order })
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, w)
	old := claimPaidCommission(t, w)
	directory := t.TempDir()
	if err := w.journal.SetCommissionFulfillmentDirectory(old.ID, directory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "report.txt"), []byte("old conclusion"), 0600); err != nil {
		t.Fatal(err)
	}
	decision := dispatch.CommissionFulfillmentDecision{Version: 1, RequestID: old.ID, OrderID: "901", OrderVersion: 3, Outcome: "artifacts_ready", Summary: "Old report", SelfCheck: "Original check", Artifacts: []dispatch.CommissionArtifact{{LogicalPath: "outputs/report.txt", RelativePath: "report.txt"}}}
	artifacts, err := validateCommissionArtifacts(directory, decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.journal.SaveCommissionCheckpoint(old.ID, dispatch.CommissionFulfillmentResult{Decision: decision, OutputDirectory: directory, Artifacts: artifacts}, "old-session"); err != nil {
		t.Fatal(err)
	}
	if err := w.journal.Update(old.ID, "failed", "commission_order_unavailable", "old-session", ""); err != nil {
		t.Fatal(err)
	}
	if err := w.journal.Retry(old.ID); err != nil {
		t.Fatal(err)
	}
	retry, ok, err := w.journal.NextKind("commission_fulfillment")
	if err != nil || !ok {
		t.Fatal("old retry missing", err)
	}
	w.runAgent = func(context.Context, dispatch.Request) (dispatch.Result, error) {
		t.Fatal("old checkpoint invoked the model")
		return dispatch.Result{}, nil
	}
	if err := w.dispatchCommissionFulfillment(context.Background(), retry); err != nil {
		t.Fatal(err)
	}
	for _, job := range w.journal.Snapshot() {
		if job.ID == old.ID && (job.Status != "needs_user" || job.Code != "commission_fulfillment_order_changed" || job.CommissionCheckpoint == nil) {
			t.Fatalf("obsolete checkpoint replayed or lost: %+v", job)
		}
	}
}

func TestCommissionReworkNoticeCopyAndDeadline(t *testing.T) {
	order := testReworkOrder()
	payload := orderNotificationPayload{RecipientRole: "seller", ToState: "in_progress", DeliveryReview: order.DeliveryReview}
	for _, language := range []string{"zh", "en"} {
		title, body := commissionDesktopCopy(language, "order.state.changed.v1", payload)
		if !strings.Contains(body, desktopDeadline(language, order.DeliveryReview.ReworkDueAt)) || commissionDesktopCopyKey("order.state.changed.v1", payload) != "delivery_changes_requested" {
			t.Fatalf("incorrect notice: %s %s", title, body)
		}
		_, card := commissionDesktopCard(language, "order.state.changed.v1", payload)
		if !strings.Contains(card, desktopCardTime(order.DeliveryReview.ReworkDueAt)) {
			t.Fatal("missing card deadline")
		}
	}
	for status, state := range map[string]string{"manual_review": "validating", "failed": "refund_pending"} {
		payload.ToState, payload.DeliveryReview.Status = state, status
		if commissionDesktopCopyKey("order.state.changed.v1", payload) == state {
			t.Fatalf("review %s hidden behind base state", status)
		}
	}
}

func TestCommissionReworkMainNoticeIncludesVerifiedReview(t *testing.T) {
	order := testReworkOrder()
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("notice attempted mutation")
			http.Error(out, "unexpected mutation", 400)
			return
		}
		if r.URL.Path == "/api/v1/orders/901" {
			materialTestResponse(out, map[string]any{"order": order})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v2/public/agents/") {
			materialTestResponse(out, map[string]any{"card": map[string]string{"agent_name": "Counterparty"}})
			return
		}
		t.Errorf("unexpected notice read %s", r.URL.Path)
		http.NotFound(out, r)
	}))
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, w)
	prompt, err := w.commissionMainNoticePrompt(context.Background(), dispatch.MainNotice{ID: "11", OrderID: "901", Version: 5, Role: "seller", Event: "order.state.changed.v1", State: "in_progress", Command: "delivery_changes_requested"})
	if err != nil {
		t.Fatal(err)
	}
	_, raw, ok := strings.Cut(prompt, "\n\nUNTRUSTED COMMISSION EVENT:\n")
	var payload struct {
		Review   *dispatch.CommissionDeliveryReview `json:"delivery_review"`
		Deadline string                             `json:"order_deadline_at_local"`
	}
	if !ok || json.Unmarshal([]byte(raw), &payload) != nil || payload.Review == nil || payload.Review.Reason != order.DeliveryReview.Reason || payload.Review.SnapshotID != order.DeliveryReview.SnapshotID || payload.Deadline != desktopDeadline("en", order.DeliveryReview.ReworkDueAt) {
		t.Fatalf("missing verified rework notice: %s", raw)
	}
}

func TestCommissionReworkReadinessRetiresOnlyAfterTerminalOrder(t *testing.T) {
	for _, terminal := range []string{"completed", "cancelled", "refunded", "rejected", "expired"} {
		t.Run(terminal, func(t *testing.T) {
			order := paidCommissionTestOrder()
			server := commissionFulfillmentServer(t, func() commissionIntakeOrder { return order })
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			installCommissionIntakeRules(t, w)
			first := claimPaidCommission(t, w)
			if err := w.journal.Update(first.ID, "completed", "commission_delivery_confirmed", "first-session", ""); err != nil {
				t.Fatal(err)
			}
			for _, state := range []string{"validating", "awaiting_buyer_confirmation", "refund_pending", terminal} {
				order.Version++
				order.State = state
				intake := claimCommissionIntakeVersion(t, w, order.Version)
				if err := w.dispatchCommissionIntake(context.Background(), intake); err != nil {
					t.Fatal(err)
				}
				paths, err := filepath.Glob(filepath.Join(w.home, "watch", "dispatch-*.json"))
				if err != nil || len(paths) != 1 {
					t.Fatal("missing fixture journal", err)
				}
				raw, err := os.ReadFile(paths[0])
				if err != nil {
					t.Fatal(err)
				}
				var journal struct {
					Active map[string]dispatch.CommissionReadiness `json:"active_commission_readiness"`
				}
				if err := json.Unmarshal(raw, &journal); err != nil {
					t.Fatal(err)
				}
				want := 1
				if state == terminal {
					want = 0
				}
				if len(journal.Active) != want {
					t.Fatalf("%s retained %d active Orders, want %d", state, len(journal.Active), want)
				}
			}
		})
	}
}
