package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/heartbeatmigration"
	watchstate "cli.eigenflux.ai/internal/watch"
)

const lifecycleThread = "01a0d200-9d41-7cd2-9214-3f04761910c4"

func lifecycleCodexBinding(t *testing.T, b dispatch.Binding) dispatch.Binding {
	t.Helper()
	b.Mode, b.Host = "native", "codex"
	b.MainSessionID = ""
	b.Env = map[string]string{"CODEX_HOME": t.TempDir()}
	dir := filepath.Join(b.Env["CODEX_HOME"], "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-date-"+lifecycleThread+".jsonl"), []byte(`{"type":"session_meta","payload":{"id":"`+lifecycleThread+`"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return b
}

func lifecycleInventory(b dispatch.Binding) heartbeatmigration.Inventory {
	return heartbeatmigration.Inventory{Complete: true, Tasks: []heartbeatmigration.Task{{ID: "host-owned-task", Name: "arbitrary title", Owner: "eigenflux", Purpose: "heartbeat", Home: b.Home, Server: b.Server, Status: "ACTIVE", Schedule: "every two hours", ThreadID: lifecycleThread}}}
}

func TestMainSessionSchedulerBootstrapUsesAccountOwnership(t *testing.T) {
	b := dispatch.Binding{Home: t.TempDir(), Server: "test"}
	for _, tc := range []struct {
		name string
		edit func(*heartbeatmigration.Inventory)
		ok   bool
	}{
		{"unique", func(_ *heartbeatmigration.Inventory) {}, true},
		{"incomplete", func(i *heartbeatmigration.Inventory) { i.Complete = false }, false},
		{"duplicate", func(i *heartbeatmigration.Inventory) {
			other := i.Tasks[0]
			other.ID = "second"
			i.Tasks = append(i.Tasks, other)
		}, false},
		{"wrong-home", func(i *heartbeatmigration.Inventory) { i.Tasks[0].Home = t.TempDir() }, false},
		{"wrong-server", func(i *heartbeatmigration.Inventory) { i.Tasks[0].Server = "other" }, false},
		{"spoofed-title", func(i *heartbeatmigration.Inventory) { i.Tasks[0].Name = "EigenFlux"; i.Tasks[0].Owner = "other" }, false},
		{"paused", func(i *heartbeatmigration.Inventory) { i.Tasks[0].Status = "PAUSED" }, false},
		{"missing-thread", func(i *heartbeatmigration.Inventory) { i.Tasks[0].ThreadID = "" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := lifecycleInventory(b)
			tc.edit(&in)
			raw, _ := json.Marshal(in)
			id, err := mainSessionFromScheduler(b, string(raw))
			if (err == nil) != tc.ok || (tc.ok && id != lifecycleThread) {
				t.Fatalf("id=%q err=%v", id, err)
			}
		})
	}
	b.MainSessionID = "existing-target"
	raw, _ := json.Marshal(lifecycleInventory(b))
	if _, err := mainSessionFromScheduler(b, string(raw)); err == nil {
		t.Fatal("scheduler silently replaced an established target")
	}
}

func TestMainSessionNewCodexBindingCapturesOnlySetupThread(t *testing.T) {
	f := newDispatchWatchFixture(t, "")
	b := lifecycleCodexBinding(t, *f.watch.binding)
	if err := os.Remove(dispatch.BindingPath(b.Home, b.Server)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_THREAD_ID", lifecycleThread)
	t.Setenv("CODEX_HOME", t.TempDir())
	file := filepath.Join(t.TempDir(), "binding.json")
	if err := dispatch.WriteJSON(file, b); err != nil {
		t.Fatal(err)
	}
	cmd, _ := bindingTestCommand()
	cmd.Flags().String("config", file, "")
	if err := watchBindCmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	got, err := dispatch.ReadBinding(b.Home, b.Server)
	if err != nil || got.MainSessionID != lifecycleThread || got.Env["CODEX_HOME"] != b.Env["CODEX_HOME"] {
		t.Fatalf("setup capture: %+v %v", got, err)
	}
	t.Setenv("CODEX_THREAD_ID", "different-heartbeat-thread")
	if err := watchBindCmd.RunE(cmd, nil); err == nil {
		t.Fatal("existing config captured a different caller")
	}
	if err := os.Remove(dispatch.BindingPath(b.Home, b.Server)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_THREAD_ID", "")
	if err := watchBindCmd.RunE(cmd, nil); err == nil {
		t.Fatal("missing setup thread accepted")
	}
}

func TestMainSessionSchedulerCommandPreservesJournalAndRejectsReplacement(t *testing.T) {
	f := newDispatchWatchFixture(t, "")
	b := lifecycleCodexBinding(t, *f.watch.binding)
	if err := dispatch.WriteJSON(dispatch.BindingPath(b.Home, b.Server), b); err != nil {
		t.Fatal(err)
	}
	job := dispatchTestNext(t, f.watch)
	cmd, _ := bindingTestCommand()
	cmd.Flags().String("scheduler-json", "", "")
	cmd.Flags().String("expected-id", "", "")
	cmd.Flags().String("expected-revision", "", "")
	raw, _ := json.Marshal(lifecycleInventory(b))
	for name, value := range map[string]string{"scheduler-json": string(raw), "expected-id": "", "expected-revision": b.Revision} {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	bad := lifecycleInventory(b)
	bad.Tasks[0].ThreadID = "01a0d200-9d41-7cd2-9214-3f04761910c5"
	badRaw, _ := json.Marshal(bad)
	if err := cmd.Flags().Set("scheduler-json", string(badRaw)); err != nil {
		t.Fatal(err)
	}
	if err := updateWatchMainSession(cmd); err == nil {
		t.Fatal("scheduler target without host metadata accepted")
	}
	unchanged, err := dispatch.ReadBinding(b.Home, b.Server)
	if err != nil || unchanged.MainSessionID != "" || unchanged.Revision != b.Revision {
		t.Fatal("failed bootstrap changed binding")
	}
	if err := cmd.Flags().Set("scheduler-json", string(raw)); err != nil {
		t.Fatal(err)
	}
	release, err := watchstate.Acquire(b.Home, b.Server)
	if err != nil {
		t.Fatal(err)
	}
	blocked := updateWatchMainSession(cmd)
	release()
	if blocked == nil {
		t.Fatal("updated while watch was active")
	}
	if err := watchBindCmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	got, err := dispatch.ReadBinding(b.Home, b.Server)
	if err != nil || got.Revision != b.Revision || got.MainSessionID != lifecycleThread {
		t.Fatalf("bootstrap: %+v %v", got, err)
	}
	jobs, err := dispatch.ReadJournalStatus(got)
	if err != nil || len(jobs) != 1 || jobs[0].ID != job.ID || jobs[0].Status != "running" {
		t.Fatalf("journal changed: %+v %v", jobs, err)
	}
	if err := cmd.Flags().Set("expected-id", lifecycleThread); err != nil {
		t.Fatal(err)
	}
	if err := updateWatchMainSession(cmd); err == nil {
		t.Fatal("scheduler used for replacement")
	}
}

func TestMainSessionHeartbeatReportsLifecycleWithoutMutation(t *testing.T) {
	f := newDispatchWatchFixture(t, "")
	b := lifecycleCodexBinding(t, *f.watch.binding)
	for _, status := range []string{"bootstrap_required", "ready", "unavailable"} {
		switch status {
		case "ready":
			b.MainSessionID = lifecycleThread
		case "unavailable":
			b.MainSessionID = "01a0d200-9d41-7cd2-9214-3f04761910c5"
		}
		path := dispatch.BindingPath(b.Home, b.Server)
		if err := dispatch.WriteJSON(path, b); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(path)
		got, err := inspectMainSessionBinding()
		if err != nil || got == nil || got.Status != status || got.ExpectedRevision != b.Revision {
			t.Fatalf("status: %+v %v", got, err)
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Fatal("heartbeat changed binding")
		}
		if text := renderHeartbeatPlanForAgent(heartbeatPlan{MainSessionBinding: got}); !strings.Contains(text, status) {
			t.Fatal("agent plan omitted binding status")
		}
	}
}
