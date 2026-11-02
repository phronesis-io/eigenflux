package cmd

import (
	"cli.eigenflux.ai/internal/client"
	"runtime"
	"strings"
	"testing"
)

func TestRuntimeEnvironmentDoesNotInferModelOrHardware(t *testing.T) {
	r := runtimeObservation(client.Meta{Host: "codex/1.2", Mode: "skill", DeviceName: "private-machine", Model: ""}, "revision")
	if r.Mode != "skill" || r.Product != "codex" || r.ProductVersion != "1.2" || r.OS != runtime.GOOS || r.Arch != runtime.GOARCH || r.Model != "" {
		t.Fatalf("unexpected environment: %+v", r)
	}
	r = runtimeObservation(client.Meta{Host: "terminal", Model: "raw secret text"}, "")
	if r.Product != "" || r.Model != "" {
		t.Fatal("inferred or raw identity")
	}
}
func TestCyclePromptDoesNotChangeBusinessOrScheduler(t *testing.T) {
	p := heartbeatPlan{CycleID: strings.Repeat("a", 32), CLIPrefix: "eigenflux --homedir /tmp/test", PlanMode: "full"}
	text := cycleInstructions(p)
	for _, v := range []string{"--cycle " + p.CycleID, "--stage <exact-stage-name>", "only after every listed stage completed", "must not repeat business work"} {
		if !strings.Contains(text, v) {
			t.Fatal(v)
		}
	}
	if cycleInstructions(heartbeatPlan{}) != "" {
		t.Fatal("old plan received invented receipt")
	}
}
