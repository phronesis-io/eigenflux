package installflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/filelock"
)

func testScope(t *testing.T) Scope {
	t.Helper()
	return Scope{Home: t.TempDir(), Endpoint: "https://install.example.test"}
}

func TestInstallFlowAnonymousRetryAndAccountFence(t *testing.T) {
	scope := testScope(t)
	s, err := Start(scope, false)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Start(scope, false)
	if err != nil || again.ID != s.ID {
		t.Fatal("start did not resume", err)
	}
	e, err := Record(scope, Event{Type: "component_install", Result: "failed", Component: "cli", Reason: "network_error"})
	if err != nil {
		t.Fatal(err)
	}
	var first Batch
	if err := Flush(scope, "", func(b Batch, h map[string]string, authenticated bool) error {
		first = b
		if authenticated || h["X-Install-Session-Proof"] != s.Proof {
			t.Fatal("unexpected delivery scope")
		}
		encoded, _ := json.Marshal(b)
		if strings.Contains(string(encoded), s.Proof) || strings.Contains(string(encoded), "agent_id") {
			t.Fatal("capability or identity in anonymous payload")
		}
		return errors.New("offline")
	}); err == nil {
		t.Fatal("offline receipt was accepted")
	}
	if err := Flush(scope, "agent-a", func(b Batch, h map[string]string, authenticated bool) error {
		if !authenticated || b.Events[0].ID != e.ID || b.Events[0].At != first.Events[0].At {
			t.Fatal("retry changed immutable event")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	called := false
	err = Flush(scope, "agent-b", func(Batch, map[string]string, bool) error { called = true; return nil })
	if !errors.Is(err, ErrScope) || called {
		t.Fatal("observations crossed accounts", err)
	}
	if _, err = RecordForAgent(scope, "agent-b", Event{Type: "credentials_saved", Result: "failed"}); !errors.Is(err, ErrScope) {
		t.Fatal("other account contaminated queue", err)
	}
	if _, err = RecordForAgent(scope, "agent-a", Event{Type: "credentials_saved", Result: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if err = Flush(scope, "agent-a", func(b Batch, _ map[string]string, _ bool) error {
		if len(b.Events) != 1 || b.Events[0].Result != "succeeded" {
			t.Fatal("account B contaminated resumed account A")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = Flush(scope, "", func(Batch, map[string]string, bool) error { t.Fatal("downgraded bound session"); return nil }); !errors.Is(err, ErrScope) {
		t.Fatal(err)
	}
}

func TestInstallFlowAuthenticatedEventsWaitForCredentials(t *testing.T) {
	scope := testScope(t)
	if _, err := Start(scope, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Record(scope, Event{Type: "baseline_feed", Result: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if err := Flush(scope, "", func(Batch, map[string]string, bool) error {
		t.Fatal("anonymous post-registration observation")
		return nil
	}); err == nil {
		t.Fatal("pending event reported as confirmed")
	}
	if err := Flush(scope, "42", func(b Batch, _ map[string]string, auth bool) error {
		if !auth || len(b.Events) != 1 {
			t.Fatal("missing queued event")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestInstallFlowBoundedAndIsolated(t *testing.T) {
	scope := testScope(t)
	if _, err := Start(scope, false); err != nil {
		t.Fatal(err)
	}
	other := scope
	other.Endpoint = "https://other.example.test"
	if _, err := Current(other); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("endpoint crossed", err)
	}
	for i := 0; i < MaxPending; i++ {
		if _, err := Record(scope, Event{Type: "host_detected", Result: "pending"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Record(scope, Event{Type: "host_detected", Result: "pending"}); err == nil {
		t.Fatal("unbounded queue")
	}
	if err := Flush(scope, "", func(b Batch, _ map[string]string, _ bool) error {
		if len(b.Events) != 20 {
			t.Fatal("batch limit")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Record(scope, Event{Type: "host_detected", Result: "pending"}); err != nil {
		t.Fatal("ack did not free queue", err)
	}
}

func TestInstallFlowRejectsSensitivePropertiesAndBadTime(t *testing.T) {
	id, _ := NewID()
	base := Event{ID: id, At: time.Now().UnixMilli(), Type: "profile_draft", Result: "succeeded"}
	bad := []Event{base, base, base, base, base}
	bad[0].Reason = "/Users/someone/private-chat.txt"
	bad[1].RuntimeName = "person@example.com"
	bad[2].Choice = "private profile contents"
	bad[3].At = time.Now().Add(-8 * 24 * time.Hour).UnixMilli()
	bad[4].At = time.Now().Add(2 * time.Minute).UnixMilli()
	for _, e := range bad {
		if Validate(e) == nil {
			t.Fatalf("accepted invalid event: %s", e.Type)
		}
	}
}

func TestInstallFlowRetainedOSLockAndSecretFile(t *testing.T) {
	scope := testScope(t)
	if _, err := Start(scope, false); err != nil {
		t.Fatal(err)
	}
	dir, _ := Directory(scope)
	info, err := os.Stat(filepath.Join(dir, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatal("proof file is not private")
	}
	lock, ok, err := filelock.TryAcquire(filepath.Join(dir, ".state.lock"))
	if err != nil || !ok {
		t.Fatal(err)
	}
	if _, err = Record(scope, Event{Type: "host_detected", Result: "pending"}); !errors.Is(err, ErrBusy) {
		t.Fatal("ignored live owner", err)
	}
	lock.Release()
	if _, err = os.Stat(filepath.Join(dir, ".state.lock")); err != nil {
		t.Fatal("lock pathname deleted")
	}
}

func TestInstallFlowDoesNotOverwriteInvalidOrLinkedState(t *testing.T) {
	scope := testScope(t)
	_, err := Start(scope, false)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := Directory(scope)
	path := filepath.Join(dir, "session.json")
	if err = os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Start(scope, true); err == nil {
		t.Fatal("invalid state silently replaced")
	}
}

func TestInstallFlowRejectsRedirectedState(t *testing.T) {
	for _, component := range []string{"install-observations", "endpoint", "session.json", ".state.lock", "queue", "event"} {
		t.Run(component, func(t *testing.T) {
			scope := testScope(t)
			session, err := Start(scope, false)
			if err != nil {
				t.Fatal(err)
			}
			event, err := Record(scope, Event{Type: "host_detected", Result: "pending"})
			if err != nil {
				t.Fatal(err)
			}
			dir, _ := Directory(scope)
			path := map[string]string{"install-observations": filepath.Dir(dir), "endpoint": dir, "session.json": filepath.Join(dir, "session.json"), ".state.lock": filepath.Join(dir, ".state.lock"), "queue": filepath.Join(dir, session.ID), "event": filepath.Join(dir, session.ID, event.ID+".json")}[component]
			target := path + "-moved"
			if err = os.Rename(path, target); err != nil {
				t.Fatal(err)
			}
			if err = os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			err = Flush(scope, "", func(Batch, map[string]string, bool) error { t.Fatal("followed redirected state"); return nil })
			if err == nil {
				t.Fatal("linked state accepted")
			}
		})
	}
}

func TestInstallFlowPrunesOnlyExpiredOwnedQueue(t *testing.T) {
	scope := testScope(t)
	session, err := Start(scope, false)
	if err != nil {
		t.Fatal(err)
	}
	event, err := Record(scope, Event{Type: "host_detected", Result: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := Directory(scope)
	queue := filepath.Join(dir, session.ID)
	old := time.Now().Add(-Retention - time.Hour)
	event.At = old.UnixMilli()
	if err = atomicJSON(filepath.Join(queue, event.ID+".json"), event); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(queue, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err = Start(scope, true); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(queue); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired queue retained", err)
	}
	if _, err = os.Stat(filepath.Join(dir, ".state.lock")); err != nil {
		t.Fatal("retained lock removed")
	}
}
