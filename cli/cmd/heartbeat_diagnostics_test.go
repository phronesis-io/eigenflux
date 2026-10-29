package cmd

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/spf13/cobra"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/selfupdate"
	"cli.eigenflux.ai/internal/skills"
)

func TestHeartbeatUpdateSkipReason(t *testing.T) {
	cfg, _ := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	if got := heartbeatUpdateSkipReason("plugin", nil); got != "configuration_unavailable" {
		t.Fatal(got)
	}
	if got := heartbeatUpdateSkipReason("", cfg); got != "unknown_integration_mode" {
		t.Fatal(got)
	}
	if err := cfg.SetKV("auto_cli_update", "false"); err != nil {
		t.Fatal(err)
	}
	if got := heartbeatUpdateSkipReason("plugin", cfg); got != "automatic_update_disabled" {
		t.Fatal(got)
	}
	if err := cfg.SetKV("auto_cli_update", "true"); err != nil {
		t.Fatal(err)
	}
	if got := heartbeatUpdateSkipReason("skill", cfg); got != "" {
		t.Fatal(got)
	}
}

func TestHeartbeatAgentShowsThrottleDeadline(t *testing.T) {
	text := renderHeartbeatPlanForAgent(heartbeatPlan{CLIUpdate: selfupdate.Result{Status: "deferred", Reason: "check_interval", NextCheckAt: "2026-10-30T12:24:00+08:00"}})
	if !strings.Contains(text, "CLI automatic update: deferred reason=check_interval next_check_at=2026-10-30T12:24:00+08:00") {
		t.Fatal(text)
	}
}

func TestLocalHeartbeatSkillsDiagnostics(t *testing.T) {
	for _, broken := range []string{"minimum", "modified", "signature"} {
		t.Run(broken, func(t *testing.T) {
			runtimeTestConfig(t, "http://127.0.0.1:1", true)
			dir := installHeartbeatTestRules(t)
			manifest, err := skills.ReadLocalManifest(dir)
			if err != nil {
				t.Fatal(err)
			}
			code := "skills_signature_failed"
			switch broken {
			case "minimum":
				pub, key, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				skills.VerifyPublicKeyBase64 = base64.StdEncoding.EncodeToString(pub)
				manifest.MinCLIVersion = "9.0.0"
				if err := skills.SignManifest(manifest, key); err != nil {
					t.Fatal(err)
				}
				code = "skills_cli_incompatible"
			case "modified":
				if err := os.WriteFile(filepath.Join(dir, "ef-broadcast", "SKILL.md"), []byte("edited"), 0600); err != nil {
					t.Fatal(err)
				}
				code = "skills_local_modified"
			case "signature":
				manifest.Signature = ""
			}
			if err := skills.WriteManifestAtomic(dir, manifest); err != nil {
				t.Fatal(err)
			}
			_, err = localHeartbeatSkills("codex")
			if err == nil || heartbeatSkillsFailureCode(err) != code {
				t.Fatalf("%v code=%s", err, heartbeatSkillsFailureCode(err))
			}
			for _, want := range []string{"CLI " + version, "required CLI", "manifest revision", manifest.Revision} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("missing %s: %v", want, err)
				}
			}
		})
	}
}

func TestMaintenanceFailureDiagnosticsAreSafeAndDeferredIsNotFailure(t *testing.T) {
	_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	var diagnostics bytes.Buffer
	recorder := newMaintenanceRecorder(server, "cli", "auto", "", &diagnostics)
	recorder.Complete(selfupdate.Result{Status: "deferred", Reason: "check_interval", Error: "previous failure"}, version)
	state, err := maintenance.Snapshot(recorder.scope)
	if err != nil || len(state.Events) != 0 || diagnostics.Len() != 0 {
		t.Fatalf("deferred counted as failed: %+v %s %v", state, diagnostics.String(), err)
	}
	recorder.Complete(selfupdate.Result{Status: "failed", Phase: "download", Reason: "download_failed", Error: "https://example.test/?token=secret"}, version)
	state, err = maintenance.Snapshot(recorder.scope)
	if err != nil || len(state.Events) != 1 || state.Events[0].ErrorCode != "download_failed" {
		t.Fatalf("failure not recorded: %+v %v", state, err)
	}
	for _, want := range []string{"[eigenflux:maintenance]", "attempt_id=" + recorder.attempt, "phase=download", "error_code=download_failed"} {
		if !strings.Contains(diagnostics.String(), want) {
			t.Fatalf("missing %s: %s", want, diagnostics.String())
		}
	}
	if strings.Contains(diagnostics.String(), "secret") {
		t.Fatal("raw error leaked")
	}
}

func TestHeartbeatSkillsFailureCodes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{fmt.Errorf("wrapped: %w", &heartbeatSkillsError{code: "skills_cli_incompatible", err: errors.New("detail")}), "skills_cli_incompatible"},
		{errors.New("skills manifest signature verification failed"), "skills_signature_failed"},
		{errors.New("signed manifest rollback rejected"), "skills_rollback_rejected"},
		{os.ErrPermission, "skills_permission_denied"},
		{errors.New("https://private.test/?token=secret"), "skills_plan_failed"},
	} {
		if got := heartbeatSkillsFailureCode(tc.err); got != tc.code {
			t.Fatalf("got %s want %s", got, tc.code)
		}
	}
}

func TestHeartbeatRejectsIncompatibleLocalSkillsWithSyncOnOrOff(t *testing.T) {
	for _, mode := range []string{"disabled", "offline", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			var events []maintenance.Event
			var mu sync.Mutex
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/maintenance/events:batch" {
					var batch struct {
						Events []maintenance.Event `json:"events"`
					}
					if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
						t.Error(err)
					}
					mu.Lock()
					events = append(events, batch.Events...)
					mu.Unlock()
				}
				_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
			}))
			defer api.Close()
			cfg, _ := runtimeTestConfig(t, api.URL, true)
			dir := installHeartbeatTestRules(t)
			pub, key, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			skills.VerifyPublicKeyBase64 = base64.StdEncoding.EncodeToString(pub)
			manifest, err := skills.ReadLocalManifest(dir)
			if err != nil {
				t.Fatal(err)
			}
			manifest.MinCLIVersion, manifest.Sequence = "9.0.0", 2
			if err := skills.SignManifest(manifest, key); err != nil {
				t.Fatal(err)
			}
			if err := skills.WriteManifestAtomic(dir, manifest); err != nil {
				t.Fatal(err)
			}
			if mode == "rollback" {
				remote := *manifest
				remote.Sequence, remote.MinCLIVersion = 1, version
				if err := skills.SignManifest(&remote, key); err != nil {
					t.Fatal(err)
				}
				cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(remote) }))
				defer cdn.Close()
				t.Setenv("EIGENFLUX_CDN_URL", cdn.URL)
			}
			if err := cfg.SetKV(autoSkillSyncKey, fmt.Sprint(mode != "disabled")); err != nil {
				t.Fatal(err)
			}
			var diagnostics bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetErr(&diagnostics)
			err = heartbeatPlanCmd.RunE(cmd, nil)
			if err == nil || heartbeatSkillsFailureCode(err) != "skills_cli_incompatible" {
				t.Fatalf("incompatible local Skills accepted: %v", err)
			}
			for _, want := range []string{"CLI " + version, "9.0.0", manifest.Revision} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("missing %s: %v", want, err)
				}
			}
			installed, readErr := skills.ReadLocalManifest(dir)
			if readErr != nil || installed.Sequence != manifest.Sequence || installed.MinCLIVersion != manifest.MinCLIVersion {
				t.Fatalf("protected manifest changed: %+v %v", installed, readErr)
			}
			mu.Lock()
			defer mu.Unlock()
			var terminal *maintenance.Event
			for i := range events {
				if events[i].Component == "skills" && events[i].Result == "failed" {
					terminal = &events[i]
				}
			}
			if terminal == nil || terminal.ErrorCode != "skills_cli_incompatible" || terminal.Phase != "check" {
				t.Fatalf("failure not observed: %+v", events)
			}
		})
	}
}

func TestHeartbeatOfflineFallbackVerifiesLocalSignature(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/agent-context" {
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(`{"error":{"code":"ONBOARDING_REQUIRED","details":{"onboarding_state":"in_progress"}}}`))
					return
				}
				_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
			}))
			defer api.Close()
			cfg, _ := runtimeTestConfig(t, api.URL, true)
			dir := installHeartbeatTestRules(t)
			manifest, err := skills.ReadLocalManifest(dir)
			if err != nil {
				t.Fatal(err)
			}
			if !valid {
				manifest.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
				if err := skills.WriteManifestAtomic(dir, manifest); err != nil {
					t.Fatal(err)
				}
			}
			if err := cfg.SetKV(autoSkillSyncKey, "true"); err != nil {
				t.Fatal(err)
			}
			if err := cfg.SetKV("auto_plugin_update", "false"); err != nil {
				t.Fatal(err)
			}
			previousFormat := formatFlag
			formatFlag = "json"
			t.Cleanup(func() { formatFlag = previousFormat })
			var diagnostics bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetErr(&diagnostics)
			out, err := captureHeartbeatStdout(t, func() error { return heartbeatPlanCmd.RunE(cmd, nil) })
			if !valid {
				if err == nil || heartbeatSkillsFailureCode(err) != "skills_signature_failed" || out != "" {
					t.Fatalf("untrusted local fallback accepted: %s %v", out, err)
				}
				for _, detail := range []string{"CLI " + version, "required CLI", manifest.Revision} {
					if !strings.Contains(err.Error(), detail) {
						t.Fatalf("missing %s: %v", detail, err)
					}
				}
				if !strings.Contains(diagnostics.String(), "phase=check error_code=skills_signature_failed") {
					t.Fatalf("missing check failure diagnostic: %s", &diagnostics)
				}
				return
			}
			if err != nil {
				t.Fatalf("signed compatible offline fallback rejected: %v", err)
			}
			var plan heartbeatPlan
			if err := json.Unmarshal([]byte(out), &plan); err != nil {
				t.Fatal(err)
			}
			if plan.SkillRevision != manifest.Revision || plan.SkillsFresh || plan.CompatibilityReported {
				t.Fatalf("invalid offline evidence: %+v", plan)
			}
		})
	}
}
