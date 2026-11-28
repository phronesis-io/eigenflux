package installhome_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type installerObservationSession struct {
	ID        string `json:"install_session_id"`
	Proof     string `json:"proof"`
	Endpoint  string `json:"endpoint"`
	CreatedAt int64  `json:"created_at"`
	AgentID   string `json:"agent_id,omitempty"`
}

func TestInstallerObservesFailureBeforeCLIExists(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX installer; PowerShell has a separate contract")
	}
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl unavailable")
	}
	for _, scenario := range []string{"download", "offline telemetry", "disabled", "existing v2", "existing legacy", "bound session", "linked state", "retry"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			agentHome := filepath.Join(home, "agent home")
			bin := filepath.Join(home, "bin")
			if err := os.MkdirAll(bin, 0700); err != nil {
				t.Fatal(err)
			}
			endpoint := "http://127.0.0.1:18089"
			digest := sha256.Sum256([]byte(endpoint))
			stateDir := filepath.Join(agentHome, "install-observations", hex.EncodeToString(digest[:]))
			writeInstallerStub(t, filepath.Join(bin, "uname"), "#!/bin/sh\ncase \"$1\" in -s) echo Linux;; -m) echo x86_64;; esac\n")
			writeInstallerStub(t, filepath.Join(bin, "curl"), `#!/bin/sh
for arg do
 case "$arg" in
  --config)
   printf '%s\n' "$*" >> "$TEST_OBSERVATION_ARGS"
   cat >> "$TEST_OBSERVATION_CONFIG"
   [ "${TEST_OBSERVATION_OFFLINE:-}" != 1 ] || exit 28
   exit 0 ;;
 esac
done
for arg do
 case "$arg" in */version.txt) printf '99.0.0\n200'; exit 0 ;; esac
done
exit 17
`)
			var previous installerObservationSession
			switch scenario {
			case "existing v2", "existing legacy":
				name := "agent-v2-credentials.json"
				if scenario == "existing legacy" {
					name = "credentials.json"
				}
				p := filepath.Join(agentHome, "servers", "default", name)
				if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("must not be read as credentials"), 0600); err != nil {
					t.Fatal(err)
				}
			case "bound session":
				previous = installerObservationSession{ID: strings.Repeat("a", 32), Proof: strings.Repeat("b", 64), Endpoint: endpoint, CreatedAt: time.Now().UnixMilli(), AgentID: "42"}
				b, _ := json.Marshal(previous)
				if err := os.MkdirAll(stateDir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stateDir, "session.json"), b, 0600); err != nil {
					t.Fatal(err)
				}
			case "linked state":
				if err := os.MkdirAll(agentHome, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(agentHome, "install-observations")); err != nil {
					t.Fatal(err)
				}
			}
			run := func() []byte {
				cmd := exec.Command("sh", "../../static/install.sh", "--host", "codex", "--homedir", agentHome)
				disabled, offline := "", ""
				if scenario == "disabled" {
					disabled = "0"
				}
				if scenario == "offline telemetry" {
					offline = "1"
				}
				cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+bin+":/usr/bin:/bin", "EIGENFLUX_HOME=", "EIGENFLUX_HOST=", "EIGENFLUX_CDN_URL=https://cdn.invalid", "EIGENFLUX_API_URL="+endpoint+"/", "EIGENFLUX_INSTALL_OBSERVATIONS="+disabled, "TEST_OBSERVATION_OFFLINE="+offline, "TEST_OBSERVATION_ARGS="+filepath.Join(home, "args"), "TEST_OBSERVATION_CONFIG="+filepath.Join(home, "config"))
				output, err := cmd.CombinedOutput()
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 17 {
					t.Fatalf("download exit code changed: %v", err)
				}
				return output
			}
			output := run()
			skip := scenario == "disabled" || scenario == "existing v2" || scenario == "existing legacy" || scenario == "linked state" || scenario == "bound session"
			if skip {
				if _, err := os.Stat(filepath.Join(home, "config")); !os.IsNotExist(err) {
					t.Fatal("observation was sent for an excluded installation")
				}
				if scenario == "bound session" {
					b, _ := os.ReadFile(filepath.Join(stateDir, "session.json"))
					var got installerObservationSession
					_ = json.Unmarshal(b, &got)
					if got != previous {
						t.Fatal("bound session changed")
					}
				}
				return
			}
			body, err := os.ReadFile(filepath.Join(stateDir, "session.json"))
			if err != nil {
				t.Fatal(err)
			}
			var session installerObservationSession
			if err = json.Unmarshal(body, &session); err != nil {
				t.Fatal(err)
			}
			if len(session.ID) != 32 || len(session.Proof) != 64 || session.Endpoint != endpoint || session.AgentID != "" {
				t.Fatal("invalid anonymous session")
			}
			if scenario == "retry" {
				run()
				after, _ := os.ReadFile(filepath.Join(stateDir, "session.json"))
				if string(after) != string(body) {
					t.Fatal("retry replaced installation session")
				}
			}
			info, _ := os.Stat(filepath.Join(stateDir, "session.json"))
			if info.Mode().Perm()&0077 != 0 {
				t.Fatal("proof is not private")
			}
			args, _ := os.ReadFile(filepath.Join(home, "args"))
			if strings.Contains(string(output), session.Proof) || strings.Contains(string(args), session.Proof) {
				t.Fatal("proof escaped to output or argv")
			}
			for _, line := range strings.Split(strings.TrimSpace(string(args)), "\n") {
				if strings.Contains(line, "--location") || strings.Contains(line, "-L") || !strings.HasPrefix(line, "-q --config - ") {
					t.Fatal("curl must disable user configuration and redirects")
				}
			}
			files, _ := filepath.Glob(filepath.Join(stateDir, session.ID, "*.json"))
			if len(files) < 4 {
				t.Fatalf("missing pre-CLI failure observations: %d", len(files))
			}
			foundFailure := false
			for _, p := range files {
				raw, _ := os.ReadFile(p)
				var event map[string]any
				if json.Unmarshal(raw, &event) != nil {
					t.Fatal("invalid queued JSON")
				}
				for key := range event {
					switch key {
					case "event_id", "event_at", "event_type", "result", "component", "runtime_name", "reason":
					default:
						t.Fatalf("unexpected property %s", key)
					}
				}
				if filepath.Base(p) != fmt.Sprint(event["event_id"])+".json" {
					t.Fatal("event ID/filename mismatch")
				}
				if event["event_type"] == "component_install" && event["result"] == "failed" && event["component"] == "cli" {
					foundFailure = true
				}
			}
			if !foundFailure {
				t.Fatal("CLI download failure not recorded")
			}
		})
	}
}

func TestInstallerConcurrentStartUsesOneSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX atomic hard-link creation")
	}
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("OpenSSL unavailable")
	}
	raw, err := os.ReadFile("../../static/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	index := strings.Index(string(raw), "# ── Main ─")
	if index < 0 {
		t.Fatal("main marker missing")
	}
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	agentHome := filepath.Join(home, "agent")
	if err = os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	writeInstallerStub(t, filepath.Join(bin, "curl"), "#!/bin/sh\ncat >/dev/null\n")
	script := filepath.Join(home, "observe.sh")
	if err = os.WriteFile(script, []byte(string(raw[:index])+"\nef_install_observe install_started started\n"), 0600); err != nil {
		t.Fatal(err)
	}
	endpoint := "http://127.0.0.1:18089"
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			c := exec.Command("sh", script, "--host", "codex", "--homedir", agentHome)
			c.Env = append(os.Environ(), "HOME="+home, "PATH="+bin+":/usr/bin:/bin", "EIGENFLUX_HOME=", "EIGENFLUX_INSTALL_OBSERVATIONS=", "EIGENFLUX_API_URL="+endpoint)
			done <- c.Run()
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	hash := sha256.Sum256([]byte(endpoint))
	dir := filepath.Join(agentHome, "install-observations", hex.EncodeToString(hash[:]))
	data, err := os.ReadFile(filepath.Join(dir, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	var session installerObservationSession
	if err = json.Unmarshal(data, &session); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != session.ID {
			t.Fatal("parallel installers created divergent queues")
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, session.ID, "*.json"))
	if len(files) != 2 {
		t.Fatalf("lost concurrent installation observations: %d", len(files))
	}
}
