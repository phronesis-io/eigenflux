package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/dispatch"
	watchstate "cli.eigenflux.ai/internal/watch"
)

func TestWatchLiveStatusRequiresFreshCurrentOwner(t *testing.T) {
	b := dispatch.Binding{Home: t.TempDir(), Server: "test", Endpoint: "https://test", Scope: "identity", Revision: "revision"}
	now := time.Now().UTC()
	s := watchLiveStatus{State: "ready", Running: true, Ready: true, AgentCallVerified: true, Home: b.Home, Server: b.Server, Endpoint: b.Endpoint, Scope: b.Scope, Revision: b.Revision, UpdatedAt: now}
	save := func() {
		t.Helper()
		raw, _ := json.Marshal(s)
		if err := os.MkdirAll(filepath.Dir(watchLivePath(b)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(watchLivePath(b), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	if got := readWatchLiveStatus(b, false, now); got.Running || got.Ready || got.AgentCallVerified {
		t.Fatalf("unlocked pulse is not live: %+v", got)
	}
	release, err := watchstate.Acquire(b.Home, b.Server)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if got := readWatchLiveStatus(b, false, now); !got.Running || !got.Ready || !got.AgentCallVerified {
		t.Fatalf("current active owner not detected: %+v", got)
	}
	for _, change := range []func(){func() { s.UpdatedAt = now.Add(-16 * time.Second) }, func() { s.Revision = "old" }, func() { s.Scope = "old" }, func() { s.Endpoint = "https://old" }, func() { s.NotificationsOnly = true }} {
		original := s
		change()
		save()
		got := readWatchLiveStatus(b, false, now)
		if got.Running || got.Ready || got.AgentCallVerified {
			t.Fatalf("stale or changed owner reported ready: %+v", got)
		}
		s = original
	}
}

func TestWatchLiveRecorderSeparatesReadinessFromModel(t *testing.T) {
	r := &watchLiveRecorder{path: filepath.Join(t.TempDir(), "status.json"), status: watchLiveStatus{State: "starting", Running: true}}
	r.event("runtime_ready", map[string]string{})
	if !r.status.Ready || r.status.AgentCallVerified {
		t.Fatal("connection readiness cannot verify a model")
	}
	for _, data := range []map[string]string{{"status": "completed", "code": "not_due"}, {"code": "commission_accept_confirmed"}, {"status": "failed", "session_id": "session"}} {
		r.event("dispatch_status", data)
		if r.status.AgentCallVerified {
			t.Fatalf("false model receipt: %v", data)
		}
	}
	r.event("dispatch_status", map[string]string{"job_id": "job", "outcome": "ready", "code": "commission_intake_ready"})
	if !r.status.AgentCallVerified {
		t.Fatal("validated intake model outcome not recorded")
	}
	r.event("runtime_unavailable", nil)
	if r.status.Ready {
		t.Fatal("disconnected owner still ready")
	}
	r.event("desktop_notification_status", map[string]string{"status": "ready"})
	r.event("desktop_notification_status", map[string]string{"notification_id": "test", "status": "submitted"})
	if r.status.DesktopStatus != "ready" {
		t.Fatal("individual toast overwrote permission status")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.pulse(ctx) }()
	for i := 0; i < 100; i++ {
		r.event("runtime_ready", nil)
		r.event("runtime_unavailable", nil)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	r.stop()
	raw, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	var stopped watchLiveStatus
	if err = json.Unmarshal(raw, &stopped); err != nil {
		t.Fatal(err)
	}
	if stopped.Running || stopped.Ready || stopped.State != "stopped" {
		t.Fatalf("stop did not persist: %+v", stopped)
	}
}

func TestWatchStatusUsesCurrentBindingRevision(t *testing.T) {
	w := newCommissionWatch(t, "https://example.test", "commission_order")
	recorder := newWatchLiveRecorder(w)
	recorder.event("runtime_ready", nil)
	recorder.mu.Lock()
	err := recorder.saveLocked()
	recorder.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	release, err := watchstate.Acquire(w.home, w.server.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var out bytes.Buffer
	previous := watchStatusCmd.OutOrStdout()
	watchStatusCmd.SetOut(&out)
	defer watchStatusCmd.SetOut(previous)
	if err := watchStatusCmd.RunE(watchStatusCmd, nil); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Runtime watchLiveStatus `json:"runtime"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Runtime.Running || !response.Runtime.Ready {
		t.Fatalf("configured binding lost live revision: %s", out.String())
	}
}
