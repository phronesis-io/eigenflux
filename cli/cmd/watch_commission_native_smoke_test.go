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
	"cli.eigenflux.ai/internal/workbuddy"
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
	runCommissionHostLocalFulfillment(t, "codex", func(binding *dispatch.Binding) {
		binding.Mode, binding.Host = "native", "codex"
		binding.Command = []string{executable}
		binding.Args = []string{"--sandbox", "workspace-write", "--ask-for-approval", "never"}
	})
}

// Real WorkBuddy model and filesystem tools; every order API and identity is an
// isolated fixture. This never creates, pays, accepts or delivers a live order.
func TestCommissionWorkBuddyHostLocalFulfillment(t *testing.T) {
	if os.Getenv("EIGENFLUX_WORKBUDDY_SMOKE") != "1" {
		t.Skip("set EIGENFLUX_WORKBUDDY_SMOKE=1 for real WorkBuddy model usage")
	}
	executable := os.Getenv("EIGENFLUX_WORKBUDDY_SMOKE_CLI")
	if !filepath.IsAbs(executable) {
		t.Fatal("EIGENFLUX_WORKBUDDY_SMOKE_CLI must select the built CLI by absolute path")
	}
	runCommissionHostLocalFulfillment(t, "WorkBuddy", func(binding *dispatch.Binding) {
		binding.Mode, binding.Host = "acp", "workbuddy"
		binding.Command = []string{executable, "watch", "workbuddy-acp"}
		for _, tool := range workbuddy.DefaultAllowedTools() {
			binding.Command = append(binding.Command, "--allow-tool", tool)
		}
		binding.Args = nil
	})
}

func runCommissionHostLocalFulfillment(t *testing.T, host string, configure func(*dispatch.Binding)) {
	t.Helper()
	order := paidCommissionTestOrder()
	order.State, order.Version = "awaiting_seller", 1
	order.BuyerInput = "Write exactly two lines: alpha and beta, in that order, each followed by a newline."
	server := commissionFulfillmentServer(t, func() commissionIntakeOrder { return order }, true)
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, w)
	configure(w.binding)
	w.binding.TimeoutSeconds = 180
	if err := dispatch.WriteJSON(dispatch.BindingPath(w.home, w.server.Name), *w.binding); err != nil {
		t.Fatal(err)
	}
	initialize := exec.Command("git", "init", "--quiet", w.binding.WorkDir)
	if output, err := initialize.CombinedOutput(); err != nil {
		t.Fatalf("isolated Git workspace: %v %s", err, output)
	}
	runner := dispatch.Runner{Binding: *w.binding}
	w.runAgent = func(ctx context.Context, request dispatch.Request) (dispatch.Result, error) {
		result, err := runner.Run(ctx, request)
		if err != nil {
			t.Logf("%s runner %s: %v", host, request.Kind, err)
		}
		return result, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	started := time.Now()
	intake := claimCommissionIntakeVersion(t, w, 1)
	if err := w.dispatchCommissionIntake(ctx, intake); err != nil {
		t.Fatal(err)
	}
	var checked dispatch.Job
	for _, job := range w.journal.Snapshot() {
		if job.ID == intake.ID {
			checked = job
		}
	}
	if checked.Status != "completed" || checked.CommissionResult == nil || checked.CommissionResult.Outcome != "ready" {
		t.Fatalf("real intake did not pass: status=%s code=%s", checked.Status, checked.Code)
	}
	t.Logf("real %s intake: %.3fs; session=%s", host, time.Since(started).Seconds(), checked.SessionID)
	order.State, order.Version = "in_progress", 3
	paid := claimCommissionIntakeVersion(t, w, 3)
	if err := w.dispatchCommissionIntake(ctx, paid); err != nil {
		t.Fatal(err)
	}
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
	if completed.Status != "completed" || completed.Code != "commission_delivery_confirmed" || completed.CommissionFulfillment == nil || completed.CommissionFulfillment.Decision.Outcome != "artifacts_ready" {
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
	t.Logf("real %s fulfillment: %.3fs; session=%s; output=%s; bytes=%d; sha256=%s; status=%s", host, time.Since(started).Seconds(), completed.SessionID, artifact.LogicalPath, artifact.ByteSize, artifact.SHA256, completed.Status)
}
