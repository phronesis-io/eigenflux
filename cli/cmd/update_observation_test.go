package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/selfupdate"
)

func TestUpdateObservationOfflineReplayAndReceipt(t *testing.T) {
	var mu sync.Mutex
	var received []maintenance.Event
	offline := true
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/v2/maintenance/events:batch" {
			t.Errorf("unexpected endpoint %s", req.URL.Path)
			http.NotFound(w, req)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if offline {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}
		var body struct {
			Events []maintenance.Event `json:"events"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		received = append(received, body.Events...)
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer api.Close()
	_, server := runtimeTestConfig(t, api.URL, true)
	var diagnostics bytes.Buffer
	recorder := newMaintenanceRecorder(server, "cli", "manual", "", &diagnostics)
	for _, phase := range []string{"check", "download", "verify", "probe", "install", "adoption"} {
		recorder.Phase(phase, "0.6.6", "0.6.7")
	}
	recorder.Complete(selfupdate.Result{Status: "updated", Phase: "adoption", Version: "0.6.7", TargetVersion: "0.6.7", Attempted: true}, "0.6.6")
	recorder.Flush()
	if got := recorder.Status().Status; got != "queued" {
		t.Fatalf("offline observation %s", got)
	}
	if !strings.Contains(diagnostics.String(), "upload_pending") {
		t.Fatal("missing offline diagnostic")
	}
	mu.Lock()
	offline = false
	mu.Unlock()
	// The next eligible invocation honors the queue's bounded backoff.
	if err := maintenance.Flush(recorder.scope, time.Now().Add(time.Minute), func(events []maintenance.Event) error {
		mu.Lock()
		received = append(received, events...)
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := recorder.Status().Status; got != "reported" {
		t.Fatalf("acknowledged observation %s", got)
	}
	recorder.Phase("check", "0.6.7", "")
	recorder.Complete(selfupdate.Result{Status: "current", Phase: "check", TargetVersion: "0.6.7", Attempted: true}, "0.6.7")
	previous, found, err := maintenance.CLIAdoptionFor(recorder.scope, recorder.principal, "0.6.7")
	if err != nil || !found || previous.AttemptID != recorder.attempt {
		t.Fatalf("later check erased receipt: %+v %v", previous, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 7 {
		t.Fatalf("got %d stages", len(received))
	}
	for i, e := range received {
		if e.AttemptID != recorder.attempt || e.Trigger != "manual" {
			t.Fatalf("incorrect attempt: %+v", e)
		}
		if i > 0 && e.EventAt <= received[i-1].EventAt {
			t.Fatal("phase order ambiguous within one recorder")
		}
	}
}

func TestUpdateObservationIdentitySwitchDoesNotSend(t *testing.T) {
	for _, change := range []string{"agent", "principal", "endpoint"} {
		t.Run(change, func(t *testing.T) {
			cfg, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
			var diagnostics bytes.Buffer
			recorder := newMaintenanceRecorder(server, "cli", "manual", "", &diagnostics)
			recorder.Phase("check", "0.6.6", "")
			creds, err := auth.LoadV2Credentials(server)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "agent":
				creds.AgentID = "another-agent"
			case "principal":
				creds.PrincipalID = "another-principal"
			case "endpoint":
				if err := cfg.UpdateServer(server, "http://127.0.0.1:2", ""); err != nil {
					t.Fatal(err)
				}
			}
			if err := auth.SaveV2Credentials(server, creds); err != nil {
				t.Fatal(err)
			}
			recorder.Phase("download", "0.6.6", "0.6.7")
			recorder.Flush()
			if got := recorder.Status().Status; got != "record_failed" {
				t.Fatalf("identity change reported %s", got)
			}
			state, err := maintenance.Snapshot(recorder.scope)
			if err != nil || len(state.Events) != 1 || state.Events[0].Phase != "check" {
				t.Fatalf("cross-identity event persisted: %+v %v", state, err)
			}
		})
	}
}

func TestUpdateObservationFailuresStayVisible(t *testing.T) {
	t.Run("no login", func(t *testing.T) {
		tempHome(t)
		recorder := newMaintenanceRecorder("absent", "cli", "manual", "", nil)
		recorder.Phase("check", "0.6.6", "")
		if got := recorder.Status().Status; got != "local_only" {
			t.Fatal(got)
		}
	})
	for _, broken := range []string{"config", "credentials", "state"} {
		t.Run(broken, func(t *testing.T) {
			_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
			var diagnostics bytes.Buffer
			recorder := newMaintenanceRecorder(server, "cli", "manual", "", &diagnostics)
			var path string
			switch broken {
			case "config":
				path = filepath.Join(recorder.scope.Home, "config.json")
			case "credentials":
				path = filepath.Join(recorder.scope.Home, "servers", server, "agent-v2-credentials.json")
			case "state":
				path = filepath.Join(recorder.scope.Home, "maintenance")
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
				t.Fatal(err)
			}
			recorder = newMaintenanceRecorder(server, "cli", "manual", "", &diagnostics)
			recorder.Phase("check", "0.6.6", "")
			if got := recorder.Status().Status; got != "record_failed" {
				t.Fatalf("%s reported %s", broken, got)
			}
			if diagnostics.Len() == 0 {
				t.Fatal("missing failure diagnostic")
			}
		})
	}
	t.Run("queue eviction", func(t *testing.T) {
		_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
		recorder := newMaintenanceRecorder(server, "cli", "manual", "", nil)
		recorder.Phase("check", "0.6.6", "")
		for i := 0; i < maintenance.MaxEvents; i++ {
			if err := maintenance.Record(recorder.scope, maintenance.NewEvent(maintenance.NewID(), "skills", "auto", "check", "started")); err != nil {
				t.Fatal(err)
			}
		}
		if got := recorder.Status(); got.Status != "record_failed" || got.ErrorCode != "maintenance_events_dropped" {
			t.Fatalf("dropped event reported delivered: %+v", got)
		}
	})
	t.Run("rollback precedes error wording", func(t *testing.T) {
		_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
		recorder := newMaintenanceRecorder(server, "cli", "manual", "", nil)
		recorder.Complete(selfupdate.Result{Status: "failed", Phase: "adoption", TargetVersion: "0.6.7", RolledBack: true, Error: "permission denied"}, "0.6.6")
		state, err := maintenance.Snapshot(recorder.scope)
		if err != nil || len(state.Events) != 1 || state.Events[0].Result != "rolled_back" {
			t.Fatalf("rollback misreported: %+v %v", state, err)
		}
	})
}
