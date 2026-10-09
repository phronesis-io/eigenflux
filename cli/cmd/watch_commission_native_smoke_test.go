package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/dispatch"
)

// Explicit opt-in: uses the operator's authenticated Codex and can consume model
// usage. All order APIs, EigenFlux credentials, inputs and outputs are fixtures.
func TestCommissionNativeCodexLocalFulfillment(t *testing.T) {
	if os.Getenv("EIGENFLUX_COMMISSION_NATIVE_SMOKE") != "1" {
		t.Skip("set EIGENFLUX_COMMISSION_NATIVE_SMOKE=1 for a real Codex invocation")
	}
	executable, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	order := paidCommissionTestOrder()
	order.BuyerInput = "Write exactly two lines: alpha and beta, in that order, each followed by a newline."
	server := commissionFulfillmentServer(t, func() commissionIntakeOrder { return order }, true)
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, w)
	w.binding.Mode, w.binding.Host = "native", "codex"
	w.binding.Command = []string{executable}
	w.binding.Args = []string{"--sandbox", "workspace-write", "--ask-for-approval", "never"}
	w.binding.TimeoutSeconds = 180
	if err := dispatch.WriteJSON(dispatch.BindingPath(w.home, w.server.Name), *w.binding); err != nil {
		t.Fatal(err)
	}
	initialize := exec.Command("git", "init", "--quiet", w.binding.WorkDir)
	if output, err := initialize.CombinedOutput(); err != nil {
		t.Fatalf("isolated Git workspace: %v %s", err, output)
	}
	w.runAgent = (dispatch.Runner{Binding: *w.binding}).Run
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	started := time.Now()
	intake := claimCommissionIntake(t, w)
	if err := w.dispatchCommissionIntake(ctx, intake); err != nil {
		t.Fatal(err)
	}
	var checked dispatch.Job
	for _, job := range w.journal.Snapshot() {
		if job.ID == intake.ID {
			checked = job
		}
	}
	if checked.CommissionResult == nil || checked.CommissionResult.Outcome != "ready" {
		t.Fatalf("real intake did not pass: status=%s code=%s", checked.Status, checked.Code)
	}
	t.Logf("real Codex intake: %.3fs; session=%s", time.Since(started).Seconds(), checked.SessionID)
	work, ok, err := w.journal.NextKind("commission_fulfillment")
	if err != nil || !ok {
		t.Fatal("paid order not queued for local work")
	}
	started = time.Now()
	if err := w.dispatchCommissionFulfillment(ctx, work); err != nil {
		t.Fatal(err)
	}
	var completed dispatch.Job
	for _, job := range w.journal.Snapshot() {
		if job.ID == work.ID {
			completed = job
		}
	}
	if completed.CommissionFulfillment == nil || completed.CommissionFulfillment.Decision.Outcome != "artifacts_ready" {
		t.Fatalf("real work did not produce verified artifacts: status=%s code=%s directory=%s", completed.Status, completed.Code, completed.CommissionDirectory)
	}
	evidence := completed.CommissionFulfillment
	if len(evidence.Artifacts) != 1 || evidence.Artifacts[0].LogicalPath != "outputs/report.txt" {
		t.Fatalf("wrong contracted manifest: %+v", evidence.Artifacts)
	}
	artifact := evidence.Artifacts[0]
	body, err := os.ReadFile(filepath.Join(evidence.OutputDirectory, artifact.RelativePath))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "alpha\nbeta\n" {
		t.Fatalf("wrong real artifact bytes: %q", strings.TrimSpace(string(body)))
	}
	t.Logf("real Codex fulfillment: %.3fs; session=%s; output=%s; bytes=%d; sha256=%s; status=%s", time.Since(started).Seconds(), completed.SessionID, artifact.LogicalPath, artifact.ByteSize, artifact.SHA256, completed.Status)
}
