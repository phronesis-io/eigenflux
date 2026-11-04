package cmd

import (
	"errors"
	"io"
	"sync"
	"testing"

	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/selfupdate"
)

func TestPolicyDiagnosticsPersistAcrossMaintenanceEntries(t *testing.T) {
	_, server := runtimeTestConfig(t, "https://example.invalid", true)
	failure := policyFailure("request", errors.New("offline"))
	recorder := func() *maintenanceRecorder { return newMaintenanceRecorder(server, "cli", "auto", "", io.Discard) }
	first := recorder()
	if !first.PolicyFailure(failure, "1.0.9") {
		t.Fatal("initial warning missing")
	}
	if recorder().PolicyFailure(failure, "1.0.9") {
		t.Fatal("unchanged warning repeated across entries")
	}
	changed := recorder()
	if !changed.PolicyFailure(policyFailure("decode", errors.New("bad body")), "1.0.9") {
		t.Fatal("changed reason suppressed")
	}
	recovered := recorder()
	recovered.PolicyRecovered("1.0.9")
	recorder().PolicyRecovered("1.0.9")
	state, err := maintenance.Snapshot(first.scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Events) != 3 {
		t.Fatalf("want warning,changed warning,recovery; got %+v", state.Events)
	}
	if state.Events[2].ErrorCode != "update_policy_recovered" || state.Events[2].Result != "no_update" {
		t.Fatalf("incorrect recovery %+v", state.Events[2])
	}
	if !recorder().PolicyFailure(failure, "1.0.9") {
		t.Fatal("new incident after recovery suppressed")
	}
	if !recorder().PolicyFailure(failure, "1.0.10") {
		t.Fatal("new CLI version warning suppressed")
	}
}

func TestPolicyDiagnosticScopeDoesNotCrossEndpoint(t *testing.T) {
	cfg, server := runtimeTestConfig(t, "https://one.invalid", true)
	failure := policyFailure("request", errors.New("offline"))
	first := newMaintenanceRecorder(server, "cli", "auto", "", io.Discard)
	if !first.PolicyFailure(failure, "1.0.9") {
		t.Fatal("initial warning missing")
	}
	if err := cfg.UpdateServer(server, "https://two.invalid", ""); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	second := newMaintenanceRecorder(server, "cli", "auto", "", io.Discard)
	second.PolicyRecovered("1.0.9")
	state, err := maintenance.Snapshot(first.scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Events) != 1 {
		t.Fatal("different endpoint recovered old incident")
	}
	if !second.PolicyFailure(failure, "1.0.9") {
		t.Fatal("different endpoint warning suppressed")
	}
}

func TestPolicyDiagnosticConcurrentEntriesEmitOneWarning(t *testing.T) {
	_, server := runtimeTestConfig(t, "https://example.invalid", true)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			newMaintenanceRecorder(server, "cli", "auto", "", io.Discard).PolicyFailure(policyFailure("request", errors.New("offline")), "1.0.9")
		}()
	}
	wg.Wait()
	r := newMaintenanceRecorder(server, "cli", "auto", "", io.Discard)
	state, err := maintenance.Snapshot(r.scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Events) != 1 {
		t.Fatalf("concurrent warning count=%d", len(state.Events))
	}
}

func TestPolicyRecoveryDoesNotReplaceBinaryCheckResult(t *testing.T) {
	_, server := runtimeTestConfig(t, "https://example.invalid", true)
	r := newMaintenanceRecorder(server, "cli", "auto", "", io.Discard)
	r.PolicyFailure(policyFailure("request", errors.New("offline")), "1.0.9")
	recovered := newMaintenanceRecorder(server, "cli", "auto", "", io.Discard)
	recovered.PolicyRecovered("1.0.9")
	recovered.Complete(selfupdate.Result{Attempted: true, Status: "no_update", Phase: "check", TargetVersion: "1.0.9"}, "1.0.9")
	state, err := maintenance.Snapshot(r.scope)
	if err != nil {
		t.Fatal(err)
	}
	var recovery, check maintenance.Event
	for _, e := range state.Events {
		if e.ErrorCode == "update_policy_recovered" {
			recovery = e
		}
		if e.AttemptID == recovered.attempt && e.Result == "no_update" {
			check = e
		}
	}
	if recovery.AttemptID == "" || check.ToVersion != "1.0.9" || recovery.AttemptID == check.AttemptID {
		t.Fatalf("recovery replaced check: %+v", state.Events)
	}
}
