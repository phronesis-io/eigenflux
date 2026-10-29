package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/profilestate"
	"github.com/spf13/cobra"
)

func TestCLIUpdatePolicyRefreshAndOfflineCache(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "v1", true: "v2"}[v2], func(t *testing.T) {
			body, status, requests := `{"code":0,"data":{}}`, http.StatusOK, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/agents/me/settings") || r.Header.Get("Authorization") == "" {
					t.Errorf("unexpected policy request: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()
			cfg, server := runtimeTestConfig(t, srv.URL, v2)
			_ = cfg.SetKV("auto_cli_update", "false")
			_ = cfg.SetServerKV(server, "auto_cli_update", "false")
			for _, tc := range []struct {
				body            string
				status          int
				enabled, failed bool
			}{
				{`offline`, 503, true, true},
				{`{"code":0,"data":{"auto_cli_update":false}}`, 200, false, false},
				{`offline`, 503, false, true},
				{`{"code":0,"data":{"auto_cli_update":"true"}}`, 200, false, true},
				{`{"code":500,"data":{"auto_cli_update":true}}`, 200, false, true},
				{`{"code":0,"data":{"auto_cli_update":true}}`, 200, true, false},
				{`offline`, 503, true, true},
				{`{"code":0,"data":{"auto_cli_update":false}}`, 200, false, false},
				{`{"code":0,"data":{}}`, 200, true, false},
			} {
				body, status = tc.body, tc.status
				enabled, err := refreshCLIUpdatePolicy(cfg, server)
				if enabled != tc.enabled || (err != nil) != tc.failed {
					t.Fatalf("body=%s enabled=%t err=%v", body, enabled, err)
				}
			}
			if requests != 9 {
				t.Fatalf("policy was throttled: %d requests", requests)
			}
		})
	}
}

func TestCLIUpdatePolicyCacheDoesNotCrossIdentity(t *testing.T) {
	cfg, server := runtimeTestConfig(t, "https://one.invalid", true)
	disabled := false
	state := profilestate.RuntimeState{CLIAutoUpdate: &disabled, CLIUpdateScope: cliUpdatePolicyScope(cfg, server)}
	if cachedCLIUpdatePolicy(cfg, server, state) {
		t.Fatal("same-scope cache ignored")
	}
	_ = cfg.UpdateServer(server, "https://two.invalid", "")
	if !cachedCLIUpdatePolicy(cfg, server, state) {
		t.Fatal("policy crossed endpoint")
	}
	_ = cfg.UpdateServer(server, "https://one.invalid", "")
	creds, err := auth.LoadV2Credentials(server)
	if err != nil {
		t.Fatal(err)
	}
	creds.AgentID = "another-agent"
	if err := auth.SaveV2Credentials(server, creds); err != nil {
		t.Fatal(err)
	}
	if !cachedCLIUpdatePolicy(cfg, server, state) {
		t.Fatal("policy crossed account")
	}
	if !cachedCLIUpdatePolicy(cfg, "another-server", state) {
		t.Fatal("policy crossed server")
	}
}

func TestCLIUpdatePolicyTimeoutKeepsLastReceivedPolicy(t *testing.T) {
	var stalled atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if stalled.Load() {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Second):
				_, _ = w.Write([]byte(`{"code":0,"data":{"auto_cli_update":true}}`))
				return
			}
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"auto_cli_update":false}}`))
	}))
	defer srv.Close()
	cfg, server := runtimeTestConfig(t, srv.URL, true)
	if enabled, err := refreshCLIUpdatePolicy(cfg, server); enabled || err != nil {
		t.Fatalf("cache setup: enabled=%t err=%v", enabled, err)
	}
	stalled.Store(true)
	if enabled, err := refreshCLIUpdatePolicy(cfg, server); enabled || err == nil || !strings.Contains(err.Error(), "Client.Timeout") {
		t.Fatalf("policy timeout ignored: enabled=%t err=%v", enabled, err)
	}
}

func TestHeartbeatServerPolicyStopsBeforeCDNAndRecordsReason(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v2/agents/me/settings" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"auto_cli_update":false}}`))
	}))
	defer srv.Close()
	cfg, server := runtimeTestConfig(t, srv.URL, true)
	clientMeta.Host, clientMeta.Mode = "codex", "skill"
	t.Setenv("EIGENFLUX_CDN_URL", srv.URL)
	t.Setenv(updateReexecEnv, "0")
	for _, minimum := range []string{"", "99.0.0"} {
		command := &cobra.Command{}
		var diagnostics bytes.Buffer
		command.SetErr(&diagnostics)
		result, restarted, err := updateHeartbeatCLI(command, cfg, minimum)
		if err != nil || restarted || result.Status != "skipped" || result.Reason != "server_policy_disabled" || result.Attempted {
			t.Fatalf("result=%+v restarted=%t err=%v", result, restarted, err)
		}
		if !strings.Contains(diagnostics.String(), "server_policy_disabled") {
			t.Fatal("missing skip diagnostic")
		}
	}
	state, err := maintenance.Snapshot(maintenance.Scope{Home: config.HomeDir(), Server: server, AgentID: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	last := state.Attempts["cli"]
	if last.Result != "blocked" || last.ErrorCode != "server_policy_disabled" || requests != 2 {
		t.Fatalf("missing policy evidence: %+v requests=%d", last, requests)
	}
}

func TestRuntimeReportObservesEffectiveBackendPolicy(t *testing.T) {
	enabled, puts := false, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["auto_cli_update"] != enabled {
				t.Errorf("wrong effective observation: %v", body)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "data": map[string]bool{"auto_cli_update": enabled}})
	}))
	defer srv.Close()
	cfg, server := runtimeTestConfig(t, srv.URL, true)
	for _, value := range []bool{false, true} {
		enabled = value
		if _, err := refreshCLIUpdatePolicy(cfg, server); err != nil {
			t.Fatal(err)
		}
		report, err := reportRuntimeSettings(cfg, "skill", "model", "codex", "", false)
		if err != nil || report.Status != "reported" {
			t.Fatalf("report=%+v err=%v", report, err)
		}
	}
	if puts != 2 {
		t.Fatal("policy change did not invalidate report cache")
	}
}

func TestSettingsSyncCannotMirrorCLIUpdatePolicyIntoKV(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("policy was pushed as mutable preference")
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"auto_cli_update":false}}`))
	}))
	defer srv.Close()
	cfg, _ := runtimeTestConfig(t, srv.URL, true)
	if err := SyncSettings(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.GetKV("auto_cli_update") != "" {
		t.Fatal("remote policy entered mutable config KV")
	}
}
