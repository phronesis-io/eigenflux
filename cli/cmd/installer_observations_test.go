package cmd

import (
	"cli.eigenflux.ai/internal/installflow"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Kept in cmd so the existing platform CI group runs the real PowerShell
// interpreter on Windows. All network and CLI work is replaced with local stubs.
func TestWindowsInstallerObservationsBeforeDownload(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires Windows PowerShell and Windows ACLs")
	}
	shell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("../../static/install.ps1")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	marker := "# ── Main ─"
	main := strings.Index(source, marker)
	if main < 0 {
		t.Fatal("installer main not found")
	}
	for _, scenario := range []string{"fresh", "retry", "disabled", "existing", "bound"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			agentHome := filepath.Join(home, "agent")
			endpoint := "http://127.0.0.1:18089"
			hash := sha256.Sum256([]byte(endpoint))
			dir := filepath.Join(agentHome, "install-observations", hex.EncodeToString(hash[:]))
			if scenario == "existing" {
				p := filepath.Join(agentHome, "servers", "default", "agent-v2-credentials.json")
				if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("do not parse"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "bound" {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				b, _ := json.Marshal(map[string]any{"install_session_id": strings.Repeat("a", 32), "proof": strings.Repeat("b", 64), "endpoint": endpoint, "created_at": time.Now().UnixMilli(), "agent_id": "42"})
				if err := os.WriteFile(filepath.Join(dir, "session.json"), b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			script := filepath.Join(home, "install-test.ps1")
			override := `
function Invoke-WebRequest {
 param($Uri,$Method,$ContentType,$Body,$Headers,$MaximumRedirection,$TimeoutSec,[switch]$UseBasicParsing)
 if ($MaximumRedirection -ne 0 -or $Uri -notlike 'http://127.0.0.1:18089/*') { throw 'Unexpected telemetry destination or redirects' }
 [IO.File]::AppendAllText($env:TEST_INSTALL_REQUESTS, "called` + "`n" + `")
 return @{ StatusCode = 202 }
}
function Install-Cli { throw 'synthetic download unavailable' }
`
			if err := os.WriteFile(script, []byte(source[:main]+override+source[main:]), 0600); err != nil {
				t.Fatal(err)
			}
			requestPath := filepath.Join(home, "requests")
			run := func() []byte {
				c := exec.Command(shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script)
				optout := ""
				if scenario == "disabled" {
					optout = "0"
				}
				c.Env = append(os.Environ(), "USERPROFILE="+home, "HOME="+home, "EIGENFLUX_HOME="+agentHome, "INVOKING_HOST=codex", "EIGENFLUX_HOST=", "EIGENFLUX_API_URL="+endpoint, "EIGENFLUX_INSTALL_OBSERVATIONS="+optout, "TEST_INSTALL_REQUESTS="+requestPath)
				out, err := c.CombinedOutput()
				if err == nil {
					t.Fatal("download failure became installer success")
				}
				if !strings.Contains(string(out), "synthetic download unavailable") {
					t.Fatal("original download error lost")
				}
				return out
			}
			output := run()
			if scenario == "disabled" || scenario == "existing" || scenario == "bound" {
				if _, err := os.Stat(requestPath); !os.IsNotExist(err) {
					t.Fatal("excluded installation sent observations")
				}
				return
			}
			raw, err := os.ReadFile(filepath.Join(dir, "session.json"))
			if err != nil {
				t.Fatal(err)
			}
			var session struct {
				ID       string `json:"install_session_id"`
				Proof    string `json:"proof"`
				Endpoint string `json:"endpoint"`
			}
			if err = json.Unmarshal(raw, &session); err != nil {
				t.Fatal(err)
			}
			if len(session.ID) != 32 || len(session.Proof) != 64 || session.Endpoint != endpoint {
				t.Fatal("invalid session")
			}
			if strings.Contains(string(output), session.Proof) {
				t.Fatal("proof leaked into installer output")
			}
			if scenario == "retry" {
				run()
				again, _ := os.ReadFile(filepath.Join(dir, "session.json"))
				if string(again) != string(raw) {
					t.Fatal("retry replaced session")
				}
			}
			files, _ := filepath.Glob(filepath.Join(dir, session.ID, "*.json"))
			if len(files) < 4 {
				t.Fatal("download failure not retained in queue")
			}
			req, err := os.ReadFile(requestPath)
			if err != nil || strings.Count(string(req), "called") < 4 {
				t.Fatal("anonymous failure was not attempted")
			}
			failed := false
			for _, p := range files {
				b, _ := os.ReadFile(p)
				var e map[string]any
				if json.Unmarshal(b, &e) != nil {
					t.Fatal("invalid event")
				}
				if e["component"] == "cli" && e["result"] == "failed" {
					failed = true
				}
			}
			if !failed {
				t.Fatal("missing failed CLI observation")
			}
		})
	}
}

func TestInstallerObservationQueueCompatibleWithCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX queue producer; native PowerShell has a separate test")
	}
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("OpenSSL unavailable")
	}
	source, err := os.ReadFile("../../static/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	main := strings.Index(string(source), "# ── Main ─")
	if main < 0 {
		t.Fatal("main marker missing")
	}
	home := t.TempDir()
	agentHome := filepath.Join(home, "agent home")
	bin := filepath.Join(home, "bin")
	if err = os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bin, "curl"), []byte("#!/bin/sh\ncat >/dev/null\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(home, "observe.sh")
	if err = os.WriteFile(script, []byte(string(source[:main])+"\nef_install_observe install_started started\nef_install_observe component_install failed cli unknown_failure\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sh", script, "--host", "codex", "--homedir", agentHome)
	c.Env = append(os.Environ(), "HOME="+home, "PATH="+bin+":/usr/bin:/bin", "EIGENFLUX_HOME=", "EIGENFLUX_INSTALL_OBSERVATIONS=", "EIGENFLUX_API_URL=http://127.0.0.1:18089/")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("observation fixture failed: %v: %s", err, out)
	}
	scope := installflow.Scope{Home: agentHome, Endpoint: "http://127.0.0.1:18089"}
	session, err := installflow.Current(scope)
	if err != nil {
		t.Fatal(err)
	}
	sent := 0
	err = installflow.Flush(scope, "42", func(batch installflow.Batch, headers map[string]string, authenticated bool) error {
		if !authenticated || batch.SessionID != session.ID || headers["X-Install-Session-Proof"] != session.Proof {
			t.Fatal("wrong shared queue scope")
		}
		for _, event := range batch.Events {
			if err := installflow.Validate(event); err != nil {
				t.Fatal(err)
			}
			if event.RuntimeName != "codex" {
				t.Fatal("host lost")
			}
			sent++
		}
		return nil
	})
	if err != nil || sent != 2 {
		t.Fatalf("CLI did not consume shell queue: count=%d err=%v", sent, err)
	}
	dir, _ := installflow.Directory(scope)
	remaining, _ := filepath.Glob(filepath.Join(dir, session.ID, "*.json"))
	if len(remaining) != 0 {
		t.Fatal("confirmed shell events remain")
	}
}
