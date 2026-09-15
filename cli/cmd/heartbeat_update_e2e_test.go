package cmd

import (
	"bytes"
	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/selfupdate"
	"cli.eigenflux.ai/internal/skills"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHeartbeatAutomaticUpgradeEndToEnd(t *testing.T) {
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
	if err := selfupdate.Sign(&release, key); err != nil {
		t.Fatal(err)
	}
	for _, forced := range []bool{false, true} {
		t.Run(map[bool]string{false: "daily", true: "Skills minimum overrides TTL"}[forced], func(t *testing.T) {
			var requests, reported atomic.Int32
			var manifest *skills.Manifest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/cli/latest/release.json":
					requests.Add(1)
					_ = json.NewEncoder(w).Encode(release)
				case "/cli/0.0.48/" + name:
					_, _ = w.Write(newBytes)
				case "/skills/latest/manifest.json":
					_ = json.NewEncoder(w).Encode(manifest)
				case "/api/v2/agent-context":
					_, _ = w.Write([]byte(`{"code":0,"data":{"context_revision":1}}`))
				case "/api/v2/agents/me/settings", "/api/v2/agent-settings/heartbeat-compatibility":
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
			_, server := runtimeTestConfig(t, srv.URL, true)
			rules := installHeartbeatTestRules(t)
			manifest, err = skills.ReadLocalManifest(rules)
			if err != nil {
				t.Fatal(err)
			}
			manifest.MinCLIVersion, manifest.Sequence = "0.0.48", 1
			// Matching revision needs no tarball; signature still verifies compatibility.
			manifest.TarSHA256 = strings.Repeat("a", 64)
			if err = skills.SignManifest(manifest, key); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(t.TempDir(), "eigenflux")
			if err = os.WriteFile(bin, oldBytes, 0755); err != nil {
				t.Fatal(err)
			}
			if forced {
				b, _ := json.Marshal(map[string]interface{}{"attempt": time.Now()})
				if err = os.WriteFile(bin+".update.json", b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(bin, "--homedir", config.HomeDir(), "--server", server, "heartbeat", "plan", "--format", "json")
			cmd.Env = append(os.Environ(), "EIGENFLUX_UPDATE_REEXEC=0", "EIGENFLUX_CDN_URL="+srv.URL, "EIGENFLUX_SKILLS_DIR="+rules, "EIGENFLUX_MODE=skill", "EIGENFLUX_HOST=codex", "EIGENFLUX_MODEL=test-model")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("heartbeat failed: %s %s %v", out, stderr.String(), err)
			}
			var plan heartbeatPlan
			if err = json.Unmarshal(out, &plan); err != nil {
				t.Fatalf("invalid output: %s %v", out, err)
			}
			if plan.CLIVersion != "0.0.48" || requests.Load() != 1 || reported.Load() == 0 || !plan.CompatibilityReported {
				t.Fatalf("upgrade/report failed: %+v requests=%d reports=%d", plan, requests.Load(), reported.Load())
			}
			creds, err := auth.LoadV2Credentials(server)
			if err != nil || creds.AgentID != "agent-1" || creds.AccessToken != "test-v2" {
				t.Fatalf("identity changed: %v", err)
			}
			if !strings.Contains(plan.SchedulerLauncher, config.HomeDir()) || !strings.Contains(plan.SchedulerLauncher, server) {
				t.Fatal("stable Home/server lost after reexec")
			}
		})
	}
}
