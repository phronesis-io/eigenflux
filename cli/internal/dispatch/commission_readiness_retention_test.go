package dispatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func acceptedReadiness(t *testing.T, j *Journal, orderID int64) (Job, CommissionReadiness) {
	t.Helper()
	mustAddCommission(t, j, commissionNotification(orderID, 1, "seller", false))
	job, ok, err := j.NextKind("commission_order")
	if err != nil || !ok {
		t.Fatalf("inspection missing: %v %v", ok, err)
	}
	value := CommissionReadiness{Result: CommissionIntakeDecision{Version: 1, RequestID: job.ID, OrderID: strconv.FormatInt(orderID, 10), OrderVersion: 1, Outcome: "ready", Summary: "Actual pre-acceptance check", InspectedFiles: []string{}}, InputFingerprint: strings.Repeat("a", 64)}
	if err := j.SaveCommissionReadiness(job.ID, value); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteCommissionIntake(job.ID, value.Result, "inspection-session"); err != nil {
		t.Fatal(err)
	}
	return job, value
}

func addCompletedReadinessNoise(t *testing.T, j *Journal, prefix string) {
	t.Helper()
	// Model a batch of ordinary completed heartbeat receipts. Persist through
	// the real journal save path to exercise compaction and restart validation.
	next := cloneJournal(j.state)
	for i := 0; i < journalMaxCompleted+10; i++ {
		next.Jobs = append(next.Jobs, Job{ID: journalID(prefix, strconv.Itoa(i)), Kind: "maintenance_due", Status: "completed", Scope: j.binding.Scope, Revision: j.binding.Revision})
	}
	if err := j.save(next); err != nil {
		t.Fatal(err)
	}
}

func TestCommissionReadinessSurvivesCompletedReceiptWindowUntilPaid(t *testing.T) {
	j := commissionJournal(t)
	source, evidence := acceptedReadiness(t, j, 901)
	addCompletedReadinessNoise(t, j, "before-payment")
	j = mustJournal(t, j.binding)
	var preserved bool
	for _, job := range j.Snapshot() {
		if job.ID == source.ID {
			preserved = job.CommissionReadiness != nil && job.CommissionReadiness.InputFingerprint == evidence.InputFingerprint
		}
	}
	if !preserved || len(j.Snapshot()) != journalMaxCompleted+1 {
		t.Fatal("ordinary receipt trimming lost the accepted order's readiness")
	}
	mustAddCommission(t, j, commissionNotification(901, 3, "seller", false))
	paid, ok, err := j.NextKind("commission_order")
	if err != nil || !ok {
		t.Fatal("payment event missing")
	}
	if err := j.QueueCommissionFromReadiness(paid.ID, source.ID, "901", 3); err != nil {
		t.Fatal(err)
	}
	if commissionReadinessCount(j.state, false) != 0 {
		t.Fatal("durable fulfillment did not release source evidence protection")
	}
	addCompletedReadinessNoise(t, j, "after-payment")
	j = mustJournal(t, j.binding)
	fulfillment, ok, err := j.NextKind("commission_fulfillment")
	if err != nil || !ok {
		t.Fatalf("paid work lost after source trimming: %v %v", ok, err)
	}
	var queued CommissionFulfillmentJob
	if err := json.Unmarshal(fulfillment.Data, &queued); err != nil || queued.InputFingerprint != evidence.InputFingerprint || queued.IntakeResult.RequestID != source.ID {
		t.Fatalf("fulfillment lost original evidence: %+v %v", queued, err)
	}
}

func TestCommissionReadinessCapacityRejectsBeforeAcceptanceAndTerminalReleases(t *testing.T) {
	j := commissionJournal(t)
	firstID := ""
	for i := 0; i < journalMaxPending; i++ {
		job, _ := acceptedReadiness(t, j, int64(1000+i))
		if firstID == "" {
			firstID = job.ID
		}
	}
	addCompletedReadinessNoise(t, j, "full-protection")
	j = mustJournal(t, j.binding)
	if commissionReadinessCount(j.state, false) != journalMaxPending {
		t.Fatal("active accepted evidence was evicted at capacity")
	}
	mustAddCommission(t, j, commissionNotification(9999, 1, "seller", false))
	newJob, ok, err := j.NextKind("commission_order")
	if err != nil || !ok {
		t.Fatal("new inspection missing")
	}
	newEvidence := CommissionReadiness{Result: CommissionIntakeDecision{Version: 1, RequestID: newJob.ID, OrderID: "9999", OrderVersion: 1, Outcome: "ready", Summary: "Actual new check", InspectedFiles: []string{}}, InputFingerprint: strings.Repeat("b", 64)}
	if err := j.SaveCommissionReadiness(newJob.ID, newEvidence); err == nil || err.Error() != "commission_readiness_capacity_exceeded" {
		t.Fatalf("acceptance gate did not reject at capacity: %v", err)
	}
	if commissionReadinessCount(j.state, false) != journalMaxPending {
		t.Fatal("a rejected sale altered existing evidence")
	}
	if err := j.RetireCommissionReadiness("1000"); err != nil {
		t.Fatal(err)
	}
	if err := j.RetireCommissionReadiness("1000"); err != nil {
		t.Fatalf("terminal release is not idempotent: %v", err)
	}
	for _, job := range j.Snapshot() {
		if job.ID == firstID {
			if job.CommissionReadiness != nil || job.CommissionResult == nil || job.CommissionResult.Outcome != "ready" {
				t.Fatal("terminal release rewrote historical inspection")
			}
		}
	}
	// This oldest historical receipt may already have fallen outside the normal
	// deduplication window; any remaining receipt must retain its original result.
	if _, err := ReadJournalStatus(j.binding); err != nil {
		t.Fatalf("released journal cannot be reopened: %v", err)
	}
	if err := j.SaveCommissionReadiness(newJob.ID, newEvidence); err != nil {
		t.Fatalf("terminal release did not free one acceptance slot: %v", err)
	}
}

func TestCommissionReadinessQueueFailureKeepsProtection(t *testing.T) {
	j := commissionJournal(t)
	source, _ := acceptedReadiness(t, j, 901)
	mustAddCommission(t, j, commissionNotification(901, 3, "seller", false))
	paid, _, err := j.NextKind("commission_order")
	if err != nil {
		t.Fatal(err)
	}
	originalPath := j.path
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	j.path = filepath.Join(blocked, "journal.json")
	if err := j.QueueCommissionFromReadiness(paid.ID, source.ID, "901", 3); err == nil {
		t.Fatal("queue unexpectedly persisted")
	}
	if commissionReadinessCount(j.state, false) != 1 || j.HasCommissionFulfillment("901") {
		t.Fatal("failed persistence released protection or partially admitted work")
	}
	if err := j.RetireCommissionReadiness("901"); err == nil {
		t.Fatal("release unexpectedly persisted")
	}
	if commissionReadinessCount(j.state, false) != 1 {
		t.Fatal("failed terminal persistence released evidence")
	}
	j.path = originalPath
	if err := j.QueueCommissionFromReadiness(paid.ID, source.ID, "901", 3); err != nil {
		t.Fatalf("could not recover failed queue write: %v", err)
	}
}
