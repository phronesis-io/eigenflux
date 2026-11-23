package dispatch

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func beginTestGeneration(t *testing.T) (*Journal, Job, CommissionFulfillmentResult, CommissionGeneration) {
	t.Helper()
	j, job, result := preparedFulfillment(t)
	identity := CommissionGeneration{LocalOnly: true, BuyerID: "41", SellerID: j.binding.AgentID, SnapshotID: "99", DeliveryDueAt: time.Now().Add(time.Hour).UnixMilli()}
	if err := j.BeginCommissionGeneration(job.ID, result.OutputDirectory, "generate", identity); err != nil {
		t.Fatal(err)
	}
	return j, job, result, identity
}

func TestCommissionGenerationPersistsBudgetAcrossRestart(t *testing.T) {
	j, job, result, identity := beginTestGeneration(t)
	for attempt := 1; attempt <= CommissionGenerationMaxAttempts; attempt++ {
		observer := GenerationRunEvidence{PID: 1234, SessionID: "codex-session"}
		if err := j.ObserveCommissionGeneration(job.ID, attempt, "generate", observer); err != nil {
			t.Fatal(err)
		}
		if err := j.BeginCommissionGeneration(job.ID, result.OutputDirectory, "generate", identity); err == nil {
			t.Fatal("an active attempt was started twice")
		}
		j = mustJournal(t, j.binding)
		claimed := claimFulfillment(t, j)
		g := claimed.CommissionGeneration
		if g == nil || g.Attempts != attempt || g.Evidence.SessionID != observer.SessionID || g.NextProbeAt != 0 {
			t.Fatalf("lost interrupted attempt: %+v", g)
		}
		if err := j.ObserveCommissionGeneration(job.ID, attempt, "generate", observer); err == nil {
			t.Fatal("late observer changed a claimed probe")
		}
		if err := j.ScheduleCommissionGenerationProbe(job.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := j.NextKind("commission_fulfillment"); err != nil || ok {
			t.Fatalf("ran probe during backoff: %v %v", ok, err)
		}
		j = mustJournal(t, j.binding)
		next := cloneJournal(j.state)
		for i := range next.Jobs {
			if next.Jobs[i].ID == job.ID {
				next.Jobs[i].CommissionGeneration.NextProbeAt = time.Now().Add(-time.Second).UnixMilli()
			}
		}
		if err := j.save(next); err != nil {
			t.Fatal(err)
		}
		g = claimFulfillment(t, j).CommissionGeneration
		if g.Attempts != attempt || g.Mode != "generate" {
			t.Fatalf("a probe spent an execution attempt: %+v", g)
		}
		err := j.BeginCommissionGeneration(job.ID, result.OutputDirectory+"-"+strconv.Itoa(attempt), "generate", identity)
		if (err == nil) != (attempt < CommissionGenerationMaxAttempts) {
			t.Fatalf("generation attempt %d replay: %v", attempt, err)
		}
	}
	if err := j.Update(job.ID, "needs_user", "commission_generation_retry_exhausted", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := j.Retry(job.ID); err != nil {
		t.Fatal(err)
	}
	if claimFulfillment(t, j).CommissionGeneration.Attempts != 3 {
		t.Fatal("ordinary retry reset the generation budget")
	}
	if err := j.Update(job.ID, "unknown", "commission_generation_unconfirmed", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := j.Reconcile(job.ID, "failed", ""); err != nil {
		t.Fatal(err)
	}
	if err := j.Retry(job.ID); err != nil {
		t.Fatal(err)
	}
	job = claimFulfillment(t, mustJournal(t, j.binding))
	if job.CommissionGeneration != nil || job.Code != "operator_retry" {
		t.Fatal("explicit operator reconciliation did not allow a fresh attempt")
	}
}

func TestCommissionGenerationReceiptSurvivesCrashAndYieldsToDelivery(t *testing.T) {
	j, job, result, identity := beginTestGeneration(t)
	for _, bad := range []string{"sensitive-bad-response", "{}"} {
		if err := j.ObserveCommissionGeneration(job.ID, 1, "generate", GenerationRunEvidence{PID: 123, SessionID: "run", Result: &Result{Text: bad}}); err != nil {
			t.Fatal(err)
		}
	}
	buf, err := os.ReadFile(j.path)
	if err != nil || strings.Contains(string(buf), "sensitive-bad-response") {
		t.Fatalf("invalid model output persisted: %v", err)
	}
	data, _ := json.Marshal(result.Decision)
	evidence := GenerationRunEvidence{PID: 123, SessionID: "run", Result: &Result{Text: string(data), SessionID: "run"}}
	if err := j.ObserveCommissionGeneration(job.ID, 1, "generate", evidence); err != nil {
		t.Fatal(err)
	}
	if err := j.ObserveCommissionGeneration(job.ID, 1, "generate", GenerationRunEvidence{PID: 123, SessionID: "other"}); err == nil {
		t.Fatal("accepted another session's observation")
	}
	if got := deliveryJob(t, j, job.ID).CommissionGeneration.Evidence.Result; got != nil {
		t.Fatal("watch status exposed model receipt")
	}
	j = mustJournal(t, j.binding)
	claimed := claimFulfillment(t, j)
	if got := claimed.CommissionGeneration.Evidence.Result; got == nil || got.Text != string(data) {
		t.Fatalf("claimed probe missing receipt: %+v", got)
	}
	retry := CommissionDeliveryRetry{Attempts: 1, BuyerID: identity.BuyerID, SellerID: identity.SellerID, SnapshotID: identity.SnapshotID}
	if err := j.SaveCommissionCheckpoint(job.ID, result, "run", retry); err != nil {
		t.Fatal(err)
	}
	got := deliveryJob(t, mustJournal(t, j.binding), job.ID)
	if got.CommissionGeneration != nil || got.CommissionCheckpoint == nil || got.Code != "commission_delivery_retry_pending" {
		t.Fatalf("delivery did not take ownership: %s", got.Code)
	}
}

func TestCommissionGenerationVerificationCannotRestartGeneration(t *testing.T) {
	j, job, result, identity := beginTestGeneration(t)
	j = mustJournal(t, j.binding)
	claimFulfillment(t, j)
	if err := j.BeginCommissionGeneration(job.ID, result.OutputDirectory, "verify", identity); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, j.binding)
	g := claimFulfillment(t, j).CommissionGeneration
	if g.Mode != "verify" || g.Attempts != 1 {
		t.Fatalf("lost verification evidence: %+v", g)
	}
	if err := j.BeginCommissionGeneration(job.ID, result.OutputDirectory, "generate", identity); err == nil {
		t.Fatal("interrupted verification restarted generation")
	}
}

func TestCommissionGenerationCannotReplayRunsWithExternalEffects(t *testing.T) {
	j, job, result := preparedFulfillment(t)
	identity := CommissionGeneration{BuyerID: "41", SellerID: j.binding.AgentID, SnapshotID: "99", LocalOnly: false}
	if err := j.BeginCommissionGeneration(job.ID, result.OutputDirectory, "generate", identity); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, j.binding)
	claimFulfillment(t, j)
	for _, localOnly := range []bool{false, true} {
		identity.LocalOnly = localOnly
		if err := j.BeginCommissionGeneration(job.ID, result.OutputDirectory, "generate", identity); err == nil {
			t.Fatalf("unrestricted original execution was replayed with local_only=%t", localOnly)
		}
	}
	if err := j.BeginCommissionGeneration(job.ID, result.OutputDirectory, "verify", identity); err != nil {
		t.Fatal(err)
	}
	if deliveryJob(t, j, job.ID).CommissionGeneration.LocalOnly {
		t.Fatal("the verifier upgraded an unrestricted original execution")
	}

	j, job, result, identity = beginTestGeneration(t)
	j = mustJournal(t, j.binding)
	claimFulfillment(t, j)
	identity.LocalOnly = false
	if err := j.BeginCommissionGeneration(job.ID, result.OutputDirectory, "generate", identity); err == nil {
		t.Fatal("admitted an unrestricted retry of a local-only generation")
	}
}

func TestCommissionGenerationUnreleasedPromptCanRestart(t *testing.T) {
	j, job, result := preparedFulfillment(t)
	identity := CommissionGeneration{BuyerID: "41", SellerID: j.binding.AgentID, SnapshotID: "99"}
	if err := j.BeginCommissionGeneration(job.ID, result.OutputDirectory, "generate", identity); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, j.binding)
	claimFulfillment(t, j)
	if err := j.BeginCommissionGeneration(job.ID, result.OutputDirectory+"-new", "generate", identity); err != nil {
		t.Fatal(err)
	}
	if err := j.ObserveCommissionGeneration(job.ID, 2, "generate", GenerationRunEvidence{PID: 1234}); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, j.binding)
	claimFulfillment(t, j)
	if err := j.BeginCommissionGeneration(job.ID, result.OutputDirectory+"-third", "generate", identity); err == nil {
		t.Fatal("replayed an unrestricted issued prompt")
	}
}
