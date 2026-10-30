package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsInstallerNotificationSetup(t *testing.T) {
	source, err := os.ReadFile("../../static/install.ps1")
	if err != nil {
		t.Fatal(err)
	}
	prefix, main, found := strings.Cut(strings.ReplaceAll(string(source), "\r\n", "\n"), "# ── Main ─")
	if !found || !strings.Contains(main, "\nSetup-Notifications\n") {
		t.Fatal("installer does not initialize notifications at completion")
	}
	for _, tc := range []struct {
		name, failure string
		calls         int
	}{
		{"ready", "", 2}, {"setup-fails", "setup", 1}, {"status-fails", "status", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			fake := filepath.Join(dir, "fake cli.cmd")
			calls := filepath.Join(dir, "calls.txt")
			stub := `@echo off
echo %*>>"%NOTIFY_FIXTURE_CALLS%"
if "%~4"=="%NOTIFY_FIXTURE_FAILURE%" exit /b 1
exit /b 0
`
			stub = strings.ReplaceAll(stub, "\n", "\r\n")
			if err := os.WriteFile(fake, []byte(stub), 0600); err != nil {
				t.Fatal(err)
			}
			script := prefix + `
function Get-InvokingHost { return 'codex' }
$script:installedCliPath=$env:NOTIFY_FIXTURE_CLI
$ExplicitEigenfluxHome=$env:NOTIFY_FIXTURE_HOME
try { Setup-Notifications } catch { Write-Output $_.Exception.Message; exit 1 }
`
			command := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
			command.Env = append(os.Environ(), "NOTIFY_FIXTURE_CLI="+fake, "NOTIFY_FIXTURE_CALLS="+calls, "NOTIFY_FIXTURE_FAILURE="+tc.failure, "NOTIFY_FIXTURE_HOME="+filepath.Join(dir, "agent home", ".eigenflux"))
			output, runErr := command.CombinedOutput()
			if (runErr == nil) != (tc.failure == "") {
				t.Fatalf("installer result: %v\n%s", runErr, output)
			}
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) != tc.calls || !strings.Contains(lines[0], "notify setup") || !strings.Contains(lines[0], "agent home") {
				t.Fatalf("incorrect setup calls: %s", data)
			}
			if tc.calls == 2 && !strings.Contains(lines[1], "notify status") {
				t.Fatalf("missing status readback: %s", data)
			}
		})
	}
}
