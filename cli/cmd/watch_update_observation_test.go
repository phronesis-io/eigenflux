package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/maintenance"
)

func TestWatchCLIExecutionIndependentOfReadiness(t *testing.T) {
	for _, mode := range []string{"ordinary", "notifications-only", "supervised"} {
		t.Run(mode, func(t *testing.T) {
			var received []maintenance.Event
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/api/v2/maintenance/events:batch" {
					t.Errorf("unexpected endpoint %s", req.URL.Path)
					http.NotFound(w, req)
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
			cfg, server := runtimeTestConfig(t, api.URL, true)
			srv, _ := cfg.GetActive(server)
			creds, _ := auth.LoadV2Credentials(server)
			watch := &accountWatch{home: config.HomeDir(), server: *srv, identity: *creds, notificationsOnly: mode == "notifications-only"}
			if mode == "supervised" {
				t.Setenv("EIGENFLUX_WATCH_SUPERVISED", "1")
			}
			// No inherited attempt environment is available after an OS-owned restart.
			t.Setenv(updateAttemptEnv, "")
			scope := maintenance.Scope{Home: watch.home, Server: server, AgentID: creds.AgentID}
			installed := maintenance.NewEvent(maintenance.NewID(), "cli", "auto", "adoption", "installed")
			installed.FromVersion, installed.ToVersion = "0.0.43", version
			if err := maintenance.RecordCLIAdoption(scope, installed, creds.PrincipalID, api.URL); err != nil {
				t.Fatal(err)
			}
			if err := maintenance.Flush(scope, time.Now(), func([]maintenance.Event) error { return nil }); err != nil {
				t.Fatal(err)
			}
			watch.reportCLIExecution(io.Discard)
			watch.reportCLIExecution(io.Discard)
			if len(received) != 1 {
				t.Fatalf("execution receipts: %+v", received)
			}
			e := received[0]
			if e.AttemptID != installed.AttemptID || e.Phase != "execute" || e.Result != "executed" || e.Trigger != "auto" || e.FromVersion != "0.0.43" || e.ToVersion != version || e.RunningVersion != version || watch.ready.Load() || watch.runtimeOnline.Load() {
				t.Fatalf("wrong execution/readiness evidence: %+v", e)
			}
			state, _ := maintenance.Snapshot(scope)
			if state.PendingCLIAdoption != nil {
				t.Fatal("execution context not consumed")
			}
		})
	}
}

func TestWatchCLIExecutionFences(t *testing.T) {
	for _, mismatch := range []string{"version", "principal", "endpoint", "agent", "trigger", "from", "current-identity"} {
		t.Run(mismatch, func(t *testing.T) {
			cfg, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
			srv, _ := cfg.GetActive(server)
			creds, _ := auth.LoadV2Credentials(server)
			watch := &accountWatch{home: config.HomeDir(), server: *srv, identity: *creds}
			scope := maintenance.Scope{Home: watch.home, Server: server, AgentID: creds.AgentID}
			installed := maintenance.NewEvent(maintenance.NewID(), "cli", "auto", "adoption", "installed")
			installed.FromVersion, installed.ToVersion = "0.0.43", version
			principal, endpoint := creds.PrincipalID, srv.Endpoint
			switch mismatch {
			case "version":
				installed.ToVersion = "9.9.9"
			case "principal":
				principal = "other-principal"
			case "endpoint":
				endpoint = "http://other.example"
			case "agent":
				scope.AgentID = "other-agent"
			case "trigger":
				installed.Trigger = "adoption"
			case "from":
				installed.FromVersion = "invalid"
			case "current-identity":
				creds.PrincipalID = "changed-after-watch-pin"
				if err := auth.SaveV2Credentials(server, creds); err != nil {
					t.Fatal(err)
				}
			}
			if err := maintenance.RecordCLIAdoption(scope, installed, principal, endpoint); err != nil {
				t.Fatal(err)
			}
			watch.reportCLIExecution(io.Discard)
			state, err := maintenance.Snapshot(scope)
			if err != nil || len(state.Events) != 1 || state.PendingCLIAdoption == nil {
				t.Fatalf("fence bypassed: %+v %v", state, err)
			}
		})
	}
}
