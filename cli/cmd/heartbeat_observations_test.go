package cmd

import (
	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/maintenance"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
	for _, v := range []string{"--cycle " + p.CycleID, "--stage <exact-stage-name>", "only after every required stage completed", "must not repeat business work"} {
		if !strings.Contains(text, v) {
			t.Fatal(v)
		}
	}
	if cycleInstructions(heartbeatPlan{}) != "" {
		t.Fatal("old plan received invented receipt")
	}
}

func TestRuntimeObservationsDoNotFollowAccountSwitch(t *testing.T) {
	_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	scope, err := maintenanceScope(server)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := auth.LoadV2Credentials(server)
	if err != nil {
		t.Fatal(err)
	}
	credentials.AgentID = "agent-2"
	if err := auth.SaveV2Credentials(server, credentials); err != nil {
		t.Fatal(err)
	}
	observeRuntimeEnvironment(scope, client.Meta{Host: "codex", Mode: "skill"})
	plan := heartbeatPlan{PlanMode: "full", ExecutionOrder: []string{"feed"}}
	attachCycleObservation(&plan, scope)
	if plan.CycleID != "" {
		t.Fatal("old plan assigned to switched account")
	}
	for _, id := range []string{scope.AgentID, "agent-2"} {
		s := scope
		s.AgentID = id
		st, err := maintenance.Snapshot(s)
		if err != nil || len(st.Events) != 0 || len(st.Cycles) != 0 {
			t.Fatalf("cross-account observations: %s %+v %v", id, st, err)
		}
	}
}

func TestEnvironmentDeduplicationUsesCohortDate(t *testing.T) {
	meta := client.Meta{Host: "codex", Mode: "skill"}
	before := time.Date(2026, 11, 2, 15, 59, 0, 0, time.UTC)
	a := runtimeEnvironmentEvent(meta, before)
	same := runtimeEnvironmentEvent(meta, before.Add(-time.Hour))
	after := runtimeEnvironmentEvent(meta, before.Add(time.Minute))
	if a.EventID != same.EventID || a.EventID == after.EventID {
		t.Fatal("environment identity does not follow UTC+8 cohort boundary")
	}
}

func TestSettingsReportDoesNotAttributeOldRequestToNewAccount(t *testing.T) {
	serverName := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			credentials, err := auth.LoadV2Credentials(serverName)
			if err != nil {
				t.Error(err)
				return
			}
			credentials.AgentID = "agent-2"
			if err := auth.SaveV2Credentials(serverName, credentials); err != nil {
				t.Error(err)
			}
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer server.Close()
	cfg, name := runtimeTestConfig(t, server.URL, true)
	serverName = name
	original, err := maintenanceScope(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reportRuntimeSettings(cfg, "skill", "model", "codex", "", true); err != nil {
		t.Fatal(err)
	}
	switched := original
	switched.AgentID = "agent-2"
	for _, scope := range []maintenance.Scope{original, switched} {
		st, err := maintenance.Snapshot(scope)
		if err != nil || len(st.Events) != 0 {
			t.Fatalf("stale request recorded under %s: %+v %v", scope.AgentID, st, err)
		}
	}
}

func TestCycleStageReceiptDefersNetworkUntilTerminal(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer server.Close()
	_, name := runtimeTestConfig(t, server.URL, true)
	scope, err := maintenanceScope(name)
	if err != nil {
		t.Fatal(err)
	}
	c, err := maintenance.StartCycle(scope, "full", []string{"feed"}, maintenance.RuntimeObservation{})
	if err != nil {
		t.Fatal(err)
	}
	command, _, err := heartbeatCmd.Find([]string{"cycle-report"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cycle", "stage", "result"} {
		old := command.Flags().Lookup(name).Value.String()
		t.Cleanup(func() { _ = command.Flags().Set(name, old) })
	}
	_ = command.Flags().Set("cycle", c.ID)
	_ = command.Flags().Set("stage", "feed")
	_ = command.Flags().Set("result", "completed")
	if err := command.RunE(command, nil); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatal("stage introduced a network round trip")
	}
	_ = command.Flags().Set("stage", "cycle")
	if err := command.RunE(command, nil); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("terminal uploads=%d", requests.Load())
	}
	st, err := maintenance.Snapshot(scope)
	if err != nil || len(st.Events) != 0 {
		t.Fatal("terminal receipt not uploaded")
	}
}
