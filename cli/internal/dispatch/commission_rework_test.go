package dispatch

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func reworkReview() *CommissionDeliveryReview {
	return &CommissionDeliveryReview{SubmissionNumber: 1, Status: "changes_requested", Reason: "The contracted conclusion is missing.", SnapshotID: json.Number("9007199254740993"), ReworkDueAt: time.Now().Add(time.Hour).UnixMilli()}
}

func queueInitialCheckedFulfillment(t *testing.T) (*Journal, Job) {
	t.Helper()
	j := commissionJournal(t)
	mustAddCommission(t, j, commissionNotification(901, 1, "seller", false))
	source, _, _ := j.NextKind("commission_order")
	value := CommissionReadiness{Result: CommissionIntakeDecision{Version: 1, RequestID: source.ID, OrderID: "901", OrderVersion: 1, Outcome: "ready", Summary: "Inputs inspected", InspectedFiles: []string{}}, InputFingerprint: strings.Repeat("a", 64)}
	if err := j.SaveCommissionReadiness(source.ID, value); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteCommissionIntake(source.ID, value.Result, "checked"); err != nil {
		t.Fatal(err)
	}
	mustAddCommission(t, j, commissionNotification(901, 3, "seller", false))
	paid, _, _ := j.NextKind("commission_order")
	if err := j.QueueCommissionFromReadiness(paid.ID, source.ID, "901", 3); err != nil {
		t.Fatal(err)
	}
	return j, claimFulfillment(t, j)
}

func TestCommissionReworkQueuesFreshIdentityAndPreservesOriginalEvidence(t *testing.T) {
	j, original := queueInitialCheckedFulfillment(t)
	if err := j.Update(original.ID, "completed", "commission_delivery_confirmed", "original-session", ""); err != nil {
		t.Fatal(err)
	}
	if !j.HasCommissionFulfillment("901") {
		t.Fatal("legacy order dedup lost")
	}
	review := reworkReview()
	mustAddCommission(t, j, commissionNotification(901, 5, "seller", false))
	intake, _, _ := j.NextKind("commission_order")
	execution := CommissionFulfillmentExecution{Revision: 8, Review: review}
	if err := j.QueueCommissionFromReadiness(intake.ID, original.ID, "901", 5, execution); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, j.binding)
	rework := claimFulfillment(t, j)
	var payload CommissionFulfillmentJob
	if err := json.Unmarshal(rework.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if rework.ID == original.ID || rework.SessionID != "" || rework.CommissionDirectory != "" || rework.CommissionCheckpoint != nil || payload.IntakeResult.OrderVersion != 1 || payload.IntakeResult.RequestID != payload.IntakeRequestID || payload.InputFingerprint != strings.Repeat("a", 64) || payload.ExecutionRevision != 8 || string(payload.DeliveryReview.SnapshotID) != "9007199254740993" {
		t.Fatalf("invalid fresh rework: %+v %+v", rework, payload)
	}
	if !j.HasCommissionFulfillment("901", payload) {
		t.Fatal("new execution not deduplicated")
	}
	for _, job := range j.Snapshot() {
		if job.ID == original.ID && (job.Status != "completed" || job.SessionID != "original-session" || job.CommissionDirectory != "") {
			t.Fatal("original job overwritten")
		}
	}
	// A duplicate event and an issue-report revision retain the same submission.
	mustAddCommission(t, j, commissionNotification(901, 5, "seller", false))
	execution.Revision++
	payload.ExecutionRevision++
	if !j.HasCommissionFulfillment("901", payload) {
		t.Fatal("issue revision bypassed dedup")
	}
	if _, ok, err := j.NextKind("commission_order"); err != nil || ok {
		t.Fatalf("duplicate event admitted: %v %v", ok, err)
	}
	if _, ok, err := j.NextKind("commission_fulfillment"); err != nil || ok {
		t.Fatalf("duplicate rework queued: %v %v", ok, err)
	}
	if _, err := j.RecoverCommission("901", 5, execution); err == nil {
		t.Fatal("current execution recovery bypassed its job")
	}
}

func TestCommissionReworkRecoveryPermitsNewVersionAndRejectsInvalidReview(t *testing.T) {
	j, original := queueInitialCheckedFulfillment(t)
	if err := j.Update(original.ID, "unknown", "interrupted_execution", "old-session", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := j.RecoverCommission("901", 5); err == nil {
		t.Fatal("ordinary intake bypassed known work")
	}
	if _, err := j.RecoverCommission("901", 5, CommissionFulfillmentExecution{Revision: 8, Review: reworkReview()}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"reviewing", "manual_review", "passed", "failed"} {
		review := reworkReview()
		review.Status = status
		if _, err := j.RecoverCommission("901", 6, CommissionFulfillmentExecution{Revision: 8, Review: review}); err == nil {
			t.Fatalf("invalid review %s admitted", status)
		}
	}
	for _, raw := range []string{`{"submission_number":1,"status":"changes_requested","reason":"Fix conclusion","snapshot_id":9007199254740993,"rework_due_at":1}`, `{"submission_number":1,"status":"changes_requested","reason":"Fix conclusion","snapshot_id":"9007199254740993","rework_due_at":1}`} {
		var review CommissionDeliveryReview
		if err := json.Unmarshal([]byte(raw), &review); err != nil || !review.AllowsRework() || string(review.SnapshotID) != "9007199254740993" {
			t.Fatalf("snapshot precision lost: %+v %v", review, err)
		}
	}
}

func TestCommissionReworkReadinessSurvivesCompletedFulfillmentCompaction(t *testing.T) {
	j, first := queueInitialCheckedFulfillment(t)
	if err := j.Update(first.ID, "completed", "commission_delivery_confirmed", "first-session", ""); err != nil {
		t.Fatal(err)
	}
	addCompletedReadinessNoise(t, j, "platform-review-pm-traffic")
	j = mustJournal(t, j.binding)
	if j.HasCommissionFulfillment("901") {
		t.Fatal("fixture did not actually trim the completed fulfillment")
	}
	sourceID, readiness := j.CommissionReworkReadiness("901", 5)
	if sourceID == "" || readiness == nil || readiness.InputFingerprint != strings.Repeat("a", 64) || readiness.Result.OrderVersion != 1 || commissionReadinessCount(j.state, false) != 1 {
		t.Fatal("in-flight Order lost its independent evidence")
	}
	readiness.Result.Summary = "reader mutation"
	_, retained := j.CommissionReworkReadiness("901", 5)
	if retained.Result.Summary == "reader mutation" {
		t.Fatal("reader changed retained evidence")
	}
	mustAddCommission(t, j, commissionNotification(901, 5, "seller", false))
	intake, _, _ := j.NextKind("commission_order")
	if err := j.QueueCommissionFromReadiness(intake.ID, sourceID, "901", 5, CommissionFulfillmentExecution{Revision: 8, Review: reworkReview()}); err != nil {
		t.Fatal(err)
	}
	rework := claimFulfillment(t, j)
	var payload CommissionFulfillmentJob
	if json.Unmarshal(rework.Data, &payload) != nil || payload.IntakeRequestID != sourceID || payload.IntakeResult.OrderVersion != 1 {
		t.Fatal("rework did not reuse the original evidence")
	}
	if err := j.RetireCommissionReadiness("901"); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, j.binding)
	if len(j.state.ActiveCommissionReadiness) != 0 || commissionReadinessCount(j.state, false) != 0 {
		t.Fatal("terminal Order did not release independent evidence")
	}
}
