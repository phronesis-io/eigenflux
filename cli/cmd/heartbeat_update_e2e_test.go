package cmd

import (
	"archive/zip"
	"bytes"
	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/selfupdate"
	"cli.eigenflux.ai/internal/skills"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHeartbeatAutomaticUpgradeEndToEnd(t *testing.T) {
	t.Setenv("EIGENFLUX_SKIP_AGENT_SETUP", "1") // Never register real OS tasks in binary-update fixtures.
	if testing.Short() {
		t.Skip("builds two real CLI executables")
	}
	if runtime.GOOS == "windows" {
		t.Skip("running executable replacement requires Windows integration host")
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	buildDir := t.TempDir()
	for _, v := range []string{"0.0.47", "0.0.48"} {
		cmd := exec.Command("go", "build", "-ldflags", "-X main.Version="+v+" -X cli.eigenflux.ai/internal/skills.VerifyPublicKeyBase64="+base64.StdEncoding.EncodeToString(pub), "-o", filepath.Join(buildDir, v), ".")
		cmd.Dir = ".."
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build CLI: %s %v", out, err)
		}
	}
	oldBytes, _ := os.ReadFile(filepath.Join(buildDir, "0.0.47"))
	newBytes, _ := os.ReadFile(filepath.Join(buildDir, "0.0.48"))
	sum := sha256.Sum256(newBytes)
	name := "eigenflux-" + runtime.GOOS + "-" + runtime.GOARCH
	release := selfupdate.Manifest{Version: "0.0.48", Artifacts: map[string]selfupdate.Artifact{name: {SHA256: hex.EncodeToString(sum[:]), Size: int64(len(newBytes))}}}
	artifactPath, artifactBytes := heartbeatUpdateArtifactFixture(t, buildDir, name, newBytes, &release, key)
	if err := selfupdate.Sign(&release, key); err != nil {
		t.Fatal(err)
	}
	t.Run("manual update without Agent login", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			switch r.URL.Path {
			case "/cli/latest/release.json", "/cli/latest/package-release.json":
				_ = json.NewEncoder(w).Encode(release)
			case "/cli/0.0.48/" + artifactPath:
				_, _ = w.Write(artifactBytes)
			default:
				t.Errorf("manual update contacted an unrelated endpoint: %s", r.URL.Path)
				http.NotFound(w, r)
			}
		}))
		defer server.Close()
		home := filepath.Join(t.TempDir(), ".eigenflux")
		if err := os.MkdirAll(home, 0700); err != nil {
			t.Fatal(err)
		}
		configBytes := []byte(`{"kv":{"auto_cli_update":"false","auto_skill_sync":"false","auto_plugin_update":"false"}}`)
		if err := os.WriteFile(filepath.Join(home, "config.json"), configBytes, 0600); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(t.TempDir(), "eigenflux")
		if err := os.WriteFile(bin, oldBytes, 0755); err != nil {
			t.Fatal(err)
		}
		saved, _ := json.Marshal(map[string]interface{}{"attempt": time.Now(), "highest": "0.0.47"})
		if err := os.WriteFile(bin+".update.json", saved, 0600); err != nil {
			t.Fatal(err)
		}
		for index, status := range []string{"restarted", "current"} {
			command := exec.Command(bin, "--homedir", home, "update", "--format", "json")
			command.Env = append(os.Environ(), "EIGENFLUX_UPDATE_REEXEC=0", "EIGENFLUX_CDN_URL="+server.URL)
			out, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("manual update failed: %s %v", out, err)
			}
			var report cliUpdateOutput
			if err := json.Unmarshal(out, &report); err != nil {
				t.Fatalf("invalid manual update result: %s %v", out, err)
			}
			if report.CLIVersion != "0.0.48" || report.CLIUpdate.Status != status || report.Home != home || report.PID <= 0 {
				t.Fatalf("missing running-process evidence: %+v", report)
			}
			if index == 0 && report.PID == command.ProcessState.Pid() {
				t.Fatal("old CLI claimed to be the replacement process")
			}
		}
		if requests.Load() != 3 {
			t.Fatalf("expected one install and one immediate current check, got %d requests", requests.Load())
		}
		assertHeartbeatUpdatedInstallation(t, bin, oldBytes, newBytes, pub)
		currentConfig, err := os.ReadFile(filepath.Join(home, "config.json"))
		if err != nil || !bytes.Equal(currentConfig, configBytes) {
			t.Fatal("manual update changed automatic-update settings")
		}
	})

	t.Run("same version bare CLI bootstraps complete package once", func(t *testing.T) {
		if runtime.GOOS != "darwin" {
			t.Skip("native complete-package bootstrap requires macOS or Windows")
		}
		var manifests, packages atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/cli/latest/package-release.json":
				manifests.Add(1)
				_ = json.NewEncoder(w).Encode(release)
			case "/cli/0.0.48/" + artifactPath:
				packages.Add(1)
				_, _ = w.Write(artifactBytes)
			case "/api/v2/agent-context":
				_, _ = w.Write([]byte(`{"code":0,"data":{"context_revision":1}}`))
			case "/api/v2/agents/me/settings", "/api/v2/agent-settings/heartbeat-compatibility", "/api/v2/maintenance/events:batch":
				_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
			default:
				t.Errorf("unexpected bootstrap request %s", r.URL.Path)
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()
		_, server := runtimeTestConfig(t, srv.URL, true)
		home := config.HomeDir()
		rules := installHeartbeatTestRules(t)
		local, err := skills.ReadLocalManifest(rules)
		if err != nil {
			t.Fatal(err)
		}
		if err := skills.SignManifest(local, key); err != nil {
			t.Fatal(err)
		}
		if err := skills.WriteManifestAtomic(rules, local); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(t.TempDir(), "eigenflux")
		if err := os.WriteFile(bin, newBytes, 0755); err != nil {
			t.Fatal(err)
		}
		// A legacy bare update already checked this exact release today. The
		// first package-capable check must still bootstrap the missing helper.
		saved, _ := json.Marshal(map[string]interface{}{"attempt": time.Now(), "highest": "0.0.48"})
		if err := os.WriteFile(bin+".update.json", saved, 0600); err != nil {
			t.Fatal(err)
		}
		envHome := t.TempDir()
		var firstPointer []byte
		for attempt, status := range []string{"restarted", "deferred"} {
			command := exec.Command(bin, "--homedir", home, "--server", server, "heartbeat", "plan", "--format", "json")
			command.Env = append(os.Environ(), "EIGENFLUX_HOME="+envHome, "EIGENFLUX_UPDATE_REEXEC=0", "EIGENFLUX_CDN_URL="+srv.URL,
				"EIGENFLUX_SKILLS_DIR="+rules, "EIGENFLUX_MODE=skill", "EIGENFLUX_HOST=codex")
			out, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("same-version bootstrap failed: %s %v", out, err)
			}
			var plan heartbeatPlan
			if err := json.Unmarshal(out, &plan); err != nil {
				t.Fatalf("invalid bootstrap plan: %s %v", out, err)
			}
			if plan.CLIVersion != "0.0.48" || plan.CLIUpdate.Status != status || !strings.Contains(plan.SchedulerLauncher, home) || !strings.Contains(plan.SchedulerLauncher, server) {
				t.Fatalf("same-version adoption lost runtime evidence: %+v", plan)
			}
			assertHeartbeatUpdatedInstallation(t, bin, newBytes, newBytes, pub)
			pointer, err := os.ReadFile(bin + ".current.json")
			if err != nil {
				t.Fatal(err)
			}
			if attempt == 0 {
				firstPointer = pointer
			} else if !bytes.Equal(firstPointer, pointer) {
				t.Fatal("second heartbeat repeated package activation")
			}
		}
		if manifests.Load() != 1 || packages.Load() != 1 {
			t.Fatalf("bootstrap repeated network checks: manifests=%d packages=%d", manifests.Load(), packages.Load())
		}
		if entries, err := os.ReadDir(envHome); err != nil || len(entries) != 0 {
			t.Fatalf("bootstrap changed environment Home: %v %v", entries, err)
		}
		credentials, err := auth.LoadV2Credentials(server)
		if err != nil || credentials.AgentID != "agent-1" || credentials.AccessToken != "test-v2" {
			t.Fatalf("bootstrap changed account identity: %v", err)
		}
	})

	t.Run("manual newer stable CLI does not forward to older active version", func(t *testing.T) {
		bin := filepath.Join(t.TempDir(), "eigenflux")
		if err := os.WriteFile(bin, newBytes, 0755); err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(bin+".versions", "0.0.47")
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "eigenflux"), oldBytes, 0755); err != nil {
			t.Fatal(err)
		}
		oldSum := sha256.Sum256(oldBytes)
		oldRelease := selfupdate.Manifest{Version: "0.0.47", Artifacts: map[string]selfupdate.Artifact{name: {SHA256: hex.EncodeToString(oldSum[:]), Size: int64(len(oldBytes))}}}
		if err := selfupdate.Sign(&oldRelease, key); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(oldRelease)
		if err := os.WriteFile(filepath.Join(directory, "release.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := selfupdate.ActivateInstallation(bin, "0.0.47", pub); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(bin, "version", "--short")
		out, err := command.CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "0.0.48" {
			t.Fatalf("manual newer CLI forwarded to the old active release: %s %v", out, err)
		}
		stable, err := os.ReadFile(bin)
		if err != nil || !bytes.Equal(stable, newBytes) {
			t.Fatal("version probe changed the manually installed CLI")
		}
		active, err := selfupdate.ResolveInstallation(bin, pub)
		if err != nil || active.Version != "0.0.47" {
			t.Fatalf("version probe rewrote the active pointer: %+v %v", active, err)
		}
	})

	t.Run("admitted bare watch internal CLI stays on its running version", func(t *testing.T) {
		bin := filepath.Join(t.TempDir(), "eigenflux")
		if err := os.WriteFile(bin, oldBytes, 0755); err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(bin+".versions", "0.0.48")
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "eigenflux"), newBytes, 0755); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(release)
		if err := os.WriteFile(filepath.Join(directory, "release.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := selfupdate.ActivateInstallation(bin, "0.0.48", pub); err != nil {
			t.Fatal(err)
		}
		root, err := filepath.EvalSymlinks(bin)
		if err != nil {
			t.Fatal(err)
		}
		baseEnv := make([]string, 0, len(os.Environ()))
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, selfupdate.InstallRootEnv+"=") && !strings.HasPrefix(value, selfupdate.InstallChildEnv+"=") {
				baseEnv = append(baseEnv, value)
			}
		}
		for index, want := range []string{"0.0.48", "0.0.47", "0.0.48"} {
			command := exec.Command(bin, "version", "--short")
			command.Env = append([]string(nil), baseEnv...)
			if index == 1 {
				command.Env = selfupdate.InstallationEnvironment(command.Env, root)
			}
			out, err := command.CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != want {
				t.Fatalf("internal pin affected the wrong invocation: index=%d want=%s output=%s err=%v", index, want, out, err)
			}
		}
		active, err := selfupdate.ResolveInstallation(bin, pub)
		if err != nil || active.Version != "0.0.48" {
			t.Fatalf("admitted child changed active installation: %+v %v", active, err)
		}
	})

	for _, broken := range []string{"credentials", "config"} {
		t.Run("manual update with unreadable "+broken, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/cli/latest/release.json", "/cli/latest/package-release.json":
					_ = json.NewEncoder(w).Encode(release)
				case "/cli/0.0.48/" + artifactPath:
					_, _ = w.Write(artifactBytes)
				default:
					t.Errorf("optional identity contacted unrelated endpoint: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			_, server := runtimeTestConfig(t, srv.URL, true)
			home := config.HomeDir()
			path := filepath.Join(home, "servers", server, "agent-v2-credentials.json")
			if broken == "config" {
				path = filepath.Join(home, "config.json")
			}
			if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(t.TempDir(), "eigenflux")
			if err := os.WriteFile(bin, oldBytes, 0755); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(bin, "--homedir", home, "--server", server, "update", "--format", "json")
			command.Env = append(os.Environ(), "EIGENFLUX_UPDATE_REEXEC=0", "EIGENFLUX_CDN_URL="+srv.URL)
			var stderr bytes.Buffer
			command.Stderr = &stderr
			out, err := command.Output()
			if err != nil {
				t.Fatalf("optional identity blocked binary update: %s %s %v", out, stderr.String(), err)
			}
			var report cliUpdateOutput
			if err := json.Unmarshal(out, &report); err != nil {
				t.Fatalf("invalid result: %s %v", out, err)
			}
			if report.CLIVersion != "0.0.48" || report.CLIUpdate.Status != "restarted" || report.Observation.Status != "record_failed" {
				t.Fatalf("binary or observation status incorrect: %+v", report)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "invalid" {
				t.Fatal("manual update modified account state")
			}
		})
	}
	for _, online := range []bool{true, false} {
		t.Run(map[bool]string{true: "authenticated manual stages", false: "authenticated manual offline stages"}[online], func(t *testing.T) {
			var mu sync.Mutex
			var observed []maintenance.Event
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/cli/latest/release.json", "/cli/latest/package-release.json":
					_ = json.NewEncoder(w).Encode(release)
				case "/cli/0.0.48/" + artifactPath:
					_, _ = w.Write(artifactBytes)
				case "/api/v2/maintenance/events:batch":
					if !online {
						http.Error(w, "offline", http.StatusServiceUnavailable)
						return
					}
					var body struct {
						Events []maintenance.Event `json:"events"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					mu.Lock()
					observed = append(observed, body.Events...)
					mu.Unlock()
					_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
				default:
					t.Errorf("unexpected manual endpoint %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			_, server := runtimeTestConfig(t, srv.URL, true)
			home := config.HomeDir()
			bin := filepath.Join(t.TempDir(), "eigenflux")
			if err := os.WriteFile(bin, oldBytes, 0755); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(bin, "--homedir", home, "--server", server, "update", "--format", "json")
			command.Env = append(os.Environ(), "EIGENFLUX_UPDATE_REEXEC=0", "EIGENFLUX_CDN_URL="+srv.URL)
			var stderr bytes.Buffer
			command.Stderr = &stderr
			out, err := command.Output()
			if err != nil {
				t.Fatalf("manual update failed: %s %s %v", out, stderr.String(), err)
			}
			var report cliUpdateOutput
			if err := json.Unmarshal(out, &report); err != nil {
				t.Fatalf("invalid result: %s %v", out, err)
			}
			wantStatus := "reported"
			if !online {
				wantStatus = "queued"
			}
			if report.CLIVersion != "0.0.48" || report.CLIUpdate.Status != "restarted" || report.Observation.Status != wantStatus {
				t.Fatalf("incorrect runtime/report status %+v; %s", report, stderr.String())
			}
			mu.Lock()
			events := append([]maintenance.Event(nil), observed...)
			mu.Unlock()
			if !online {
				state, err := maintenance.Snapshot(maintenance.Scope{Home: home, Server: server, AgentID: "agent-1"})
				if err != nil {
					t.Fatal(err)
				}
				events = state.Events
			}
			assertCLIUpdateStages(t, events, "manual", report.Observation.AttemptID)
			creds, err := auth.LoadV2Credentials(server)
			if err != nil || creds.AgentID != "agent-1" || creds.AccessToken != "test-v2" {
				t.Fatalf("identity changed: %v", err)
			}
		})
	}
	for _, tc := range []struct {
		mode, host             string
		forced, contextFailure bool
		retry                  bool
		policyDisabled         bool
	}{
		{mode: "skill", host: "codex"}, {mode: "skill", host: "codex", forced: true},
		{mode: "plugin", host: "claude-code"}, {mode: "plugin", host: "claude-code", forced: true},
		{mode: "plugin", host: "openclaw"}, {mode: "plugin", host: "openclaw", forced: true},
		{mode: "skill", host: "codex", forced: true, contextFailure: true},
		{mode: "skill", host: "codex", retry: true},
		{mode: "skill", host: "codex", policyDisabled: true},
	} {
		caseName := map[bool]string{false: "daily", true: "Skills minimum overrides TTL"}[tc.forced]
		if tc.contextFailure {
			caseName += " with replacement business failure"
		}
		if tc.retry {
			caseName = "automatic retry after failed check"
		}
		if tc.policyDisabled {
			caseName = "server pause and resume automatic upgrade"
		}
		t.Run(tc.host+"/"+caseName, func(t *testing.T) {
			var requests, reported atomic.Int32
			var policyDisabled atomic.Bool
			policyDisabled.Store(tc.policyDisabled)
			var eventMu sync.Mutex
			var observations []maintenance.Event
			var manifest *skills.Manifest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/cli/latest/release.json", "/cli/latest/package-release.json":
					requests.Add(1)
					_ = json.NewEncoder(w).Encode(release)
				case "/cli/0.0.48/" + artifactPath:
					_, _ = w.Write(artifactBytes)
				case "/skills/latest/manifest.json":
					_ = json.NewEncoder(w).Encode(manifest)
				case "/api/v2/agent-context":
					if tc.contextFailure {
						http.Error(w, "business unavailable after CLI replacement", http.StatusServiceUnavailable)
						return
					}
					_, _ = w.Write([]byte(`{"code":0,"data":{"context_revision":1}}`))
				case "/api/v2/maintenance/events:batch":
					var body struct {
						Events []maintenance.Event `json:"events"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					eventMu.Lock()
					observations = append(observations, body.Events...)
					eventMu.Unlock()
					_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
				case "/api/v2/agents/me/settings", "/api/v2/agent-settings/heartbeat-compatibility":
					if r.Method == http.MethodGet {
						_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "data": map[string]bool{"auto_cli_update": !policyDisabled.Load()}})
						return
					}
					if r.Method == http.MethodPut || r.Method == http.MethodPost {
						if r.Header.Get("X-Client-CLI-Version") == "0.0.48" {
							reported.Add(1)
						}
					}
					_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			cfg, server := runtimeTestConfig(t, srv.URL, true)
			if err := cfg.SetKV("auto_cli_update", "false"); err != nil {
				t.Fatal(err)
			}
			if err := cfg.SetServerKV(server, "auto_cli_update", "false"); err != nil {
				t.Fatal(err)
			}
			// Native OpenClaw maintenance has its own fake-manager integration tests.
			if tc.host == "openclaw" {
				if err := cfg.SetKV("auto_plugin_update", "false"); err != nil {
					t.Fatal(err)
				}
			}
			if err := cfg.SetKV(autoSkillSyncKey, "true"); err != nil {
				t.Fatal(err)
			}
			rules := installHeartbeatTestRules(t)
			manifest, err = skills.ReadLocalManifest(rules)
			if err != nil {
				t.Fatal(err)
			}
			// Both real CLI versions trust the same test publisher. The generic
			// rules helper has its own key, so re-sign its installed manifest
			// before simulating an update with matching content and sequence.
			if err = skills.SignManifest(manifest, key); err != nil {
				t.Fatal(err)
			}
			if err = skills.WriteManifestAtomic(rules, manifest); err != nil {
				t.Fatal(err)
			}
			manifest.MinCLIVersion, manifest.Sequence = "0.0.48", 1
			if tc.policyDisabled {
				manifest.MinCLIVersion = ""
			}
			// Matching revision needs no tarball; signature still verifies compatibility.
			manifest.TarSHA256 = strings.Repeat("a", 64)
			if err = skills.SignManifest(manifest, key); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(t.TempDir(), "eigenflux")
			if err = os.WriteFile(bin, oldBytes, 0755); err != nil {
				t.Fatal(err)
			}
			if tc.forced {
				b, _ := json.Marshal(map[string]interface{}{"attempt": time.Now()})
				if err = os.WriteFile(bin+".update.json", b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.retry {
				b, _ := json.Marshal(map[string]interface{}{"attempt": time.Now().Add(-5 * time.Minute), "error": "previous check failed"})
				if err = os.WriteFile(bin+".update.json", b, 0600); err != nil {
					t.Fatal(err)
				}
				// Keep the minimum unchanged so it cannot bypass the retry interval.
				manifest.MinCLIVersion = ""
				if err = skills.SignManifest(manifest, key); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(bin, "--homedir", config.HomeDir(), "--server", server, "heartbeat", "plan", "--format", "json")
			envHome := t.TempDir()
			cmd.Env = append(os.Environ(), "EIGENFLUX_HOME="+envHome, "EIGENFLUX_UPDATE_REEXEC=0", "EIGENFLUX_CDN_URL="+srv.URL, "EIGENFLUX_SKILLS_DIR="+rules, "EIGENFLUX_MODE="+tc.mode, "EIGENFLUX_HOST="+tc.host, "EIGENFLUX_MODEL=test-model")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if tc.contextFailure {
				if err == nil {
					t.Fatal("replacement business failure was hidden")
				}
				eventMu.Lock()
				captured := append([]maintenance.Event(nil), observations...)
				eventMu.Unlock()
				assertCLIUpdateStages(t, captured, "auto", "")
				localState, snapshotErr := maintenance.Snapshot(maintenance.Scope{Home: config.HomeDir(), Server: server, AgentID: "agent-1"})
				if snapshotErr != nil {
					t.Fatal(snapshotErr)
				}
				completed := localState.Attempts["cli"]
				if completed.Phase != "execute" || completed.Result != "executed" || completed.RunningVersion != "0.0.48" {
					t.Fatalf("business failure overwrote confirmed CLI adoption: %+v", completed)
				}
				for _, event := range append(captured, localState.Events...) {
					if event.Component == "cli" && event.Result == "failed" {
						t.Fatalf("business failure falsely failed CLI update: %+v", event)
					}
				}
				if !strings.Contains(stderr.String(), "error_code=process_exit_failed") {
					t.Fatalf("replacement exit diagnostic missing: %s", &stderr)
				}
				validated := false
				for _, event := range captured {
					if event.Component != "skills" {
						continue
					}
					if event.Result == "failed" {
						t.Fatalf("child business failure falsely failed parent Skills: %+v", event)
					}
					validated = validated || event.Result == "no_update"
				}
				if !validated {
					t.Fatalf("replacement Skills check did not complete: %+v", captured)
				}
				assertHeartbeatUpdatedInstallation(t, bin, oldBytes, newBytes, pub)
				if requests.Load() != 1 {
					t.Fatal("business failure affected successful CLI replacement")
				}
				return
			}
			if err != nil {
				t.Fatalf("heartbeat failed: %s %s %v", out, stderr.String(), err)
			}
			if tc.policyDisabled {
				var paused heartbeatPlan
				if err := json.Unmarshal(out, &paused); err != nil {
					t.Fatal(err)
				}
				if paused.CLIVersion != "0.0.47" || paused.CLIUpdate.Reason != "server_policy_disabled" || requests.Load() != 0 {
					t.Fatalf("backend pause ignored: %+v requests=%d", paused.CLIUpdate, requests.Load())
				}
				if _, err := os.Stat(bin + ".update.json"); !os.IsNotExist(err) {
					t.Fatalf("policy pause changed binary check stamp: %v", err)
				}
				eventMu.Lock()
				blocked := false
				for _, event := range observations {
					blocked = blocked || (event.Component == "cli" && event.Result == "blocked" && event.ErrorCode == "server_policy_disabled" && event.ToVersion == "")
				}
				observations = nil
				eventMu.Unlock()
				if !blocked {
					t.Fatal("missing backend-policy pause observation")
				}
				policyDisabled.Store(false)
				resumed := exec.Command(bin, cmd.Args[1:]...)
				resumed.Env, resumed.Stderr = cmd.Env, &stderr
				out, err = resumed.Output()
				if err != nil {
					t.Fatalf("resume heartbeat failed: %s %s %v", out, stderr.String(), err)
				}
			}
			if entries, err := os.ReadDir(envHome); err != nil || len(entries) != 0 {
				t.Fatalf("environment Home changed by update probes: %v %v", entries, err)
			}
			var plan heartbeatPlan
			if err = json.Unmarshal(out, &plan); err != nil {
				t.Fatalf("invalid output: %s %v", out, err)
			}
			if plan.CLIVersion != "0.0.48" || requests.Load() != 1 || reported.Load() == 0 || !plan.CompatibilityReported {
				t.Fatalf("upgrade/report failed: %+v requests=%d reports=%d", plan, requests.Load(), reported.Load())
			}
			eventMu.Lock()
			captured := append([]maintenance.Event(nil), observations...)
			eventMu.Unlock()
			assertCLIUpdateStages(t, captured, "auto", "")
			creds, err := auth.LoadV2Credentials(server)
			if err != nil || creds.AgentID != "agent-1" || creds.AccessToken != "test-v2" {
				t.Fatalf("identity changed: %v", err)
			}
			if !strings.Contains(plan.SchedulerLauncher, config.HomeDir()) || !strings.Contains(plan.SchedulerLauncher, server) {
				t.Fatal("stable Home/server lost after reexec")
			}
			stableBin, stableErr := filepath.EvalSymlinks(bin)
			if stableErr != nil {
				t.Fatal(stableErr)
			}
			expectedLauncher, launcherErr := renderHeartbeatLauncher(stableBin, config.HomeDir(), server, tc.mode, "")
			if launcherErr != nil || plan.SchedulerLauncher != expectedLauncher {
				t.Fatalf("integration mode changed after upgrade: got=%q want=%q err=%v", plan.SchedulerLauncher, expectedLauncher, launcherErr)
			}
			if tc.host == "openclaw" && plan.PluginMaintenance.Status != "not_applicable" {
				t.Fatal("binary fixture started real host plugin maintenance")
			}
			if tc.host != "openclaw" && (plan.PluginMaintenance.Host != tc.host || !plan.PluginMaintenance.Due || !strings.Contains(plan.AgentPrompt, `"plugin_id":"`+heartbeatPluginID(tc.host)+`"`)) {
				t.Fatalf("shared plan omitted host plugin maintenance: %+v", plan.PluginMaintenance)
			}
			if tc.mode == "plugin" && !strings.Contains(plan.SchedulerMigration, "do not create a second heartbeat") {
				t.Fatal("plugin loop lost scheduler ownership")
			}
		})
	}
}

func assertCLIUpdateStages(t *testing.T, events []maintenance.Event, trigger, attempt string) {
	t.Helper()
	phases := map[string]bool{}
	for _, event := range events {
		if event.Component != "cli" {
			continue
		}
		if attempt == "" {
			attempt = event.AttemptID
		}
		if event.AttemptID != attempt || event.Trigger != trigger {
			t.Fatalf("update correlation lost: %+v", event)
		}
		if err := maintenance.Validate(event); err != nil {
			t.Fatalf("invalid wire event: %+v %v", event, err)
		}
		if event.Phase == "check" && event.Result == "started" {
			if event.ToVersion != "" {
				t.Fatal("manifest target trusted before signature check")
			}
		} else if event.ToVersion != "0.0.48" {
			t.Fatalf("signed target missing: %+v", event)
		}
		if event.Result == "executed" && event.RunningVersion != "0.0.48" {
			t.Fatal("old process claimed adoption")
		}
		phases[event.Phase+"/"+event.Result] = true
	}
	for _, key := range []string{"check/started", "download/started", "verify/started", "probe/started", "install/started", "adoption/started", "adoption/installed", "execute/started", "execute/executed"} {
		if !phases[key] {
			t.Fatalf("missing %s: %+v", key, events)
		}
	}
}

// A complete fixture is built once and reused by every simulated CDN scenario.
// The real ad-hoc signed helper exercises the same verification as a local build.
func heartbeatUpdateArtifactFixture(t *testing.T, buildDir, name string, binary []byte, release *selfupdate.Manifest, key ed25519.PrivateKey) (string, []byte) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return name, binary
	}
	directory := filepath.Join(buildDir, "complete-package")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "eigenflux"), binary, 0755); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("bash", "../scripts/build-notifier-macos.sh", directory, runtime.GOARCH)
	build.Env = append(os.Environ(), "EIGENFLUX_NOTIFIER_VERSION="+release.Version, "EIGENFLUX_NOTIFIER_SIGN_IDENTITY=-")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build notification fixture: %s %v", out, err)
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	err := filepath.Walk(directory, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || path == directory {
			return walkErr
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		header.Name, header.Method = filepath.ToSlash(relative), zip.Deflate
		if info.IsDir() {
			header.Name += "/"
		}
		entry, err := writer.CreateHeader(header)
		if err != nil || info.IsDir() {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(entry, file)
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data := archive.Bytes()
	sum := sha256.Sum256(data)
	release.Packages = map[string]selfupdate.Artifact{name + ".zip": {Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}}
	if err := selfupdate.Sign(release, key); err != nil {
		t.Fatal(err)
	}
	return name + ".zip", data
}

func assertHeartbeatUpdatedInstallation(t *testing.T, stable string, oldBytes, newBytes []byte, key ed25519.PublicKey) {
	t.Helper()
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		entry, err := os.ReadFile(stable)
		if err != nil || !bytes.Equal(entry, oldBytes) {
			t.Fatal("complete upgrade changed the stable bootstrap")
		}
		active, err := selfupdate.ResolveInstallation(stable, key)
		if err != nil || active.Version != "0.0.48" {
			t.Fatalf("active release=%+v err=%v", active, err)
		}
		installed, err := os.ReadFile(active.Executable)
		if err != nil || !bytes.Equal(installed, newBytes) {
			t.Fatal("complete upgrade did not activate verified new CLI bytes")
		}
		if _, err := os.Stat(stable + ".previous"); !os.IsNotExist(err) {
			t.Fatal("complete upgrade unexpectedly created an in-place binary backup")
		}
		return
	}
	installed, _ := os.ReadFile(stable)
	previous, _ := os.ReadFile(stable + ".previous")
	if !bytes.Equal(installed, newBytes) || !bytes.Equal(previous, oldBytes) {
		t.Fatal("binary upgrade failed to retain old and adopt new bytes")
	}
}
