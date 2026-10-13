package watchservice

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The restart helper must never become an installer, reconfigure a task, or
// launch an elevated shell. Keep this boundary checked on non-Windows CI too.
func TestWindowsRestartScriptPreservesTaskDefinition(t *testing.T) {
	script, err := os.ReadFile("restart_windows.ps1")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		`(?i)\b(RegisterTask(?:Definition)?|DeleteTask|Register-ScheduledTask|Set-ScheduledTask|Unregister-ScheduledTask)\b`,
		`(?i)\b(Invoke-Expression|Start-Process|Set-ExecutionPolicy)\b`,
		`(?i)\.Definition(?:\.[A-Za-z]+)+\s*=[^=]`,
	} {
		if matched := regexp.MustCompile(forbidden).Find(script); matched != nil {
			t.Fatalf("restart helper crosses its lifecycle-only boundary: %q", matched)
		}
	}
}

// Exercise the actual script against an in-memory scheduler. No task is
// registered, stopped, or launched on the machine running this test.
func TestWindowsRestartScript(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires Windows PowerShell and Windows identity APIs")
	}
	powershell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, err := os.Stat(powershell); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("restart_windows.ps1")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		taskName string
		scenario string
		wantErr  string
		wantOps  string
	}{
		{"restart", `\EigenFlux\watch`, "running", "", "stop\nrun"},
		{"unicode", `\EigenFlux\外循环`, "running", "", "stop\nrun"},
		{"already_stopped", `\EigenFlux\watch`, "stopped", "", "run"},
		{"literal_name", `\EigenFlux $(throw 'injected')`, "running", "", "stop\nrun"},
		{"wildcard", `\EigenFlux*`, "running", "invalid_task_name", ""},
		{"relative_path", `\EigenFlux\..\other`, "running", "invalid_task_name", ""},
		{"missing", `\EigenFlux\watch`, "missing", "task_not_found", ""},
		{"other_user", `\EigenFlux\watch`, "other_user", "task_identity", ""},
		{"noninteractive", `\EigenFlux\watch`, "noninteractive", "interactive_required", ""},
		{"elevated", `\EigenFlux\watch`, "elevated", "elevation_required", ""},
		{"disabled", `\EigenFlux\watch`, "disabled", "task_disabled", ""},
		{"no_demand_start", `\EigenFlux\watch`, "no_demand_start", "demand_start_disabled", ""},
		{"stop_failure", `\EigenFlux\watch`, "stop_failure", "stop_failed", "stop"},
		{"start_failure", `\EigenFlux\watch`, "start_failure", "start_failed", "stop\nrun"},
		{"exits_during_start", `\EigenFlux\watch`, "exited", "process_exited", "stop\nrun"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trace := filepath.Join(t.TempDir(), "operations.txt")
			taskPath := tc.taskName
			if !strings.HasPrefix(taskPath, `\`) {
				taskPath = `\` + taskPath
			}
			preamble := fmt.Sprintf(windowsMockScheduler,
				base64.StdEncoding.EncodeToString([]byte(taskPath)), tc.scenario)
			input, err := json.Marshal(map[string]string{"task_name": tc.taskName})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", preamble+string(script))
			command.Env = append(os.Environ(), "EIGENFLUX_WATCH_TEST_TRACE="+trace)
			command.Stdin = bytes.NewReader(input)
			output, runErr := command.CombinedOutput()
			var result struct {
				Status string `json:"status"`
				Target string `json:"target"`
				Error  string `json:"error"`
			}
			if err := json.Unmarshal(output, &result); err != nil {
				t.Fatalf("invalid response: %s: %v (%v)", output, err, runErr)
			}
			if result.Error != tc.wantErr || (runErr == nil) != (tc.wantErr == "") {
				t.Fatalf("result=%+v exit=%v; want error %q", result, runErr, tc.wantErr)
			}
			if tc.wantErr == "" && (result.Status != "process_started" || result.Target != taskPath) {
				t.Fatalf("incorrect success: %+v", result)
			}
			operations, err := os.ReadFile(trace)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(strings.ReplaceAll(string(operations), "\r\n", "\n")); got != tc.wantOps {
				t.Fatalf("operations %q; want %q", got, tc.wantOps)
			}
		})
	}
}

const windowsMockScheduler = `
$ErrorActionPreference = 'Stop'
$script:expectedPath = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('%s'))
$script:scenario = '%s'
$script:instances = 1
if ($script:scenario -eq 'stopped') { $script:instances = 0 }
$script:principal = [pscustomobject]@{
    UserId = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    LogonType = 3
    RunLevel = 0
}
if ($script:scenario -eq 'other_user') { $script:principal.UserId = 'S-1-5-18' }
if ($script:scenario -eq 'noninteractive') { $script:principal.LogonType = 2 }
if ($script:scenario -eq 'elevated') { $script:principal.RunLevel = 1 }
$script:task = [pscustomobject]@{
    Path = $script:expectedPath
    Enabled = ($script:scenario -ne 'disabled')
    Definition = [pscustomobject]@{
        Principal = $script:principal
        Settings = [pscustomobject]@{ AllowDemandStart = ($script:scenario -ne 'no_demand_start') }
        Actions = 'original executable, arguments and working directory'
    }
}
$script:task | Add-Member ScriptMethod Stop {
    param($flags)
    Add-Content -LiteralPath $env:EIGENFLUX_WATCH_TEST_TRACE -Value 'stop'
    if ($script:scenario -eq 'stop_failure') { throw 'stop failed' }
    $script:instances = 0
}
$script:task | Add-Member ScriptMethod GetInstances {
    param($flags)
    return [pscustomobject]@{ Count = $script:instances }
}
$script:task | Add-Member ScriptMethod Run {
    param($parameters)
    Add-Content -LiteralPath $env:EIGENFLUX_WATCH_TEST_TRACE -Value 'run'
    if ($script:instances -ne 0) { throw 'old instance not stopped' }
    if ($null -ne $parameters -or $this.Definition.Actions -ne 'original executable, arguments and working directory') { throw 'changed action' }
    if ($script:scenario -eq 'start_failure') { throw 'start failed' }
    $instance = [pscustomobject]@{ State = 4; RefreshCount = 0 }
    $instance | Add-Member ScriptMethod Refresh {
        $this.RefreshCount++
        if ($script:scenario -eq 'exited' -and $this.RefreshCount -gt 1) { $this.State = 3 }
    }
    return $instance
}
$script:folder = [pscustomobject]@{}
$script:folder | Add-Member ScriptMethod GetTask {
    param($name)
    if ($name -cne $script:expectedPath -or $script:scenario -eq 'missing') { throw 'not found' }
    return $script:task
}
$script:service = [pscustomobject]@{}
$script:service | Add-Member ScriptMethod Connect { }
$script:service | Add-Member ScriptMethod GetFolder {
    param($name)
    if ($name -ne '\') { throw 'unexpected folder' }
    return $script:folder
}
function New-Object {
    param([string]$ComObject)
    if ($ComObject -ne 'Schedule.Service') { throw 'unexpected COM object' }
    return $script:service
}
`
