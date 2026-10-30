package dispatch

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCommissionExecutionOutboxRestartRecoveryAndStaleAcknowledgement(t *testing.T) {
	j := commissionJournal(t)
	mustAddCommission(t, j, commissionNotification(901, 3, "seller", true))
	job, _, _ := j.NextKind("commission_order")
	if err := j.SetCommissionExecution(job.ID, "902", 3, 0); err == nil {
		t.Fatal("cross-order context accepted")
	}
	if err := j.SetCommissionExecution(job.ID, "901", 3, 4); err != nil {
		t.Fatal(err)
	}
	if reports := j.CommissionExecutionReports(); len(reports) != 0 {
		t.Fatal("running work reported as blocked")
	}
	if err := j.Update(job.ID, "needs_user", "commission_skill_missing", "", ""); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, j.binding)
	reports := j.CommissionExecutionReports()
	if len(reports) != 1 || reports[0].Code != "skill_missing" || reports[0].ExpectedExecutionRevision != 4 {
		t.Fatal(reports)
	}
	old := reports[0]
	raw, _ := json.Marshal(old)
	if strings.Contains(string(raw), "buyer_input") || strings.Contains(string(raw), "local_files") {
		t.Fatal("private input in report")
	}
	if err := j.Retry(job.ID); err != nil {
		t.Fatal(err)
	}
	job, _, _ = j.NextKind("commission_order")
	if err := j.SetCommissionExecution(job.ID, "901", 3, 5); err != nil {
		t.Fatal(err)
	}
	if err := j.Update(job.ID, "needs_user", "commission_skill_unreadable", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := j.AcknowledgeCommissionExecution(old); err != nil {
		t.Fatal(err)
	}
	reports = j.CommissionExecutionReports()
	if len(reports) != 1 || reports[0].ExpectedExecutionRevision != 5 || reports[0].Code != "skill_unreadable" {
		t.Fatal("late receipt consumed newer report", reports)
	}
	if err := j.AcknowledgeCommissionExecution(reports[0]); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, j.binding)
	if len(j.CommissionExecutionReports()) != 0 {
		t.Fatal("acknowledged report replayed")
	}
	if err := j.Retry(job.ID); err != nil {
		t.Fatal(err)
	}
	job, _, _ = j.NextKind("commission_order")
	if err := j.SetCommissionExecution(job.ID, "901", 3, 6, true); err != nil {
		t.Fatal(err)
	}
	result := CommissionIntakeDecision{Version: 1, RequestID: job.ID, OrderID: "901", OrderVersion: 3, Outcome: "ready", Summary: "checked", InspectedFiles: []string{}}
	if err := j.CompleteCommissionIntake(job.ID, result, ""); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, j.binding)
	reports = j.CommissionExecutionReports()
	if len(reports) != 1 || reports[0].Status != "resolved" || reports[0].Code != "" || reports[0].ExpectedExecutionRevision != 6 {
		t.Fatal("recovery lost during completed-job compaction", reports)
	}
	// A status reader cannot mutate durable fence state.
	snapshot := j.Snapshot()
	snapshot[0].CommissionExecution.OrderID = "invalid"
	if j.CommissionExecutionReports()[0].OrderID != "901" {
		t.Fatal("mutable context escaped")
	}
}

func TestCommissionExecutionVerifiedReconcileUsesAcknowledgedFenceOnce(t *testing.T) {
	j := commissionJournal(t)
	mustAddCommission(t, j, commissionNotification(901, 3, "seller", true))
	job, _, _ := j.NextKind("commission_order")
	if err := j.SetCommissionExecution(job.ID, "901", 3, 4); err != nil {
		t.Fatal(err)
	}
	if err := j.Update(job.ID, "unknown", "interrupted_execution", "", ""); err != nil {
		t.Fatal(err)
	}
	blocked := j.CommissionExecutionReports()[0]
	if err := j.AcknowledgeCommissionExecution(blocked, 5); err != nil {
		t.Fatal(err)
	}
	if len(j.CommissionExecutionReports()) != 0 {
		t.Fatal("blocked receipt changed its own key")
	}
	if err := j.Reconcile(job.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	resolved := j.CommissionExecutionReports()
	if len(resolved) != 1 || resolved[0].ExpectedExecutionRevision != 5 || resolved[0].Status != "resolved" {
		t.Fatal(resolved)
	}
	if err := j.AcknowledgeCommissionExecution(resolved[0], 6); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, j.binding)
	if len(j.CommissionExecutionReports()) != 0 {
		t.Fatal("resolved receipt endlessly advanced its fence")
	}
}

func TestCommissionExecutionRecoveryPreferredDuringReceiptCompaction(t *testing.T) {
	job := Job{ID: strings.Repeat("a", 64), Kind: "commission_order", Status: "completed", Code: "operator_verified", CommissionExecution: &CommissionExecutionContext{OrderID: "901", OrderVersion: 3, Stage: "intake", MayResolve: true}}
	s := journalState{Jobs: []Job{job}}
	for i := 0; i < journalMaxCompleted; i++ {
		s.Jobs = append(s.Jobs, Job{ID: fmt.Sprint(i), Status: "completed"})
	}
	trimCompleted(&s)
	if len(s.Jobs) != journalMaxCompleted || s.Jobs[0].ID != job.ID {
		t.Fatal("unreported recovery evicted before ordinary receipts")
	}
}

func TestCommissionExecutionCodeIsBoundedAndPMExcluded(t *testing.T) {
	if CommissionExecutionCode("private /path token", "unknown") != "execution_unconfirmed" || CommissionExecutionCode("raw private error", "failed") != "execution_failed" {
		t.Fatal("raw error escaped category")
	}
	j := mustJournal(t, journalBinding(t))
	mustAdd(t, j, "pm")
	job := mustNext(t, j)
	if err := j.SetCommissionExecution(job.ID, "901", 3, 0); err == nil {
		t.Fatal("PM acquired an order report")
	}
	if err := j.Update(job.ID, "needs_user", "agent_decision", "", ""); err != nil {
		t.Fatal(err)
	}
	if len(j.CommissionExecutionReports()) != 0 {
		t.Fatal("PM reported")
	}
}
