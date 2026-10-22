package watchservice

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWindowsNotificationTaskLifecycle(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PowerShell identity required")
	}
	script, err := os.ReadFile("start_windows.ps1")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"new", "running", "named_user", "disabled", "other_user", "exited", "upgrade", "prepare"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			request := map[string]string{"task_name": "EigenFlux-Notifications-" + strings.Repeat("a", 32), "scope": strings.Repeat("a", 32),
				"wrapper": filepath.Join(dir, "中文 $(throw 'injected').ps1"), "request_file": filepath.Join(dir, "request.json"), "home": dir}
			if scenario == "prepare" {
				request["prepare_only"] = "true"
			}
			if scenario == "running" || scenario == "named_user" {
				old, _ := json.Marshal(request)
				os.WriteFile(request["request_file"], old, 0600)
				os.WriteFile(request["wrapper"], []byte("# test wrapper"), 0600)
			}
			input, _ := json.Marshal(map[string]interface{}{"request": request, "wrapper_content": "# test wrapper"})
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			ps := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
			cmd := exec.CommandContext(ctx, ps, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "$script:scenario = '"+scenario+"'\n"+notificationSchedulerMock+string(script))
			cmd.Stdin = bytes.NewReader(input)
			output, runErr := cmd.CombinedOutput()
			var result struct {
				Status string `json:"status"`
				Error  string `json:"error"`
			}
			if err := json.Unmarshal(output, &result); err != nil {
				t.Fatalf("%s %v", output, err)
			}
			want := map[string]string{"disabled": "task_disabled", "other_user": "task_ownership", "exited": "start_failed"}[scenario]
			if result.Error != want || (runErr == nil) != (want == "") {
				t.Fatalf("result=%+v err=%v", result, runErr)
			}
			_, fileErr := os.Stat(request["wrapper"])
			if scenario == "running" || scenario == "named_user" || scenario == "disabled" || scenario == "other_user" {
				if (scenario == "disabled" || scenario == "other_user") && !os.IsNotExist(fileErr) {
					t.Fatal("mutated disabled/foreign task launcher")
				}
			} else if scenario != "prepare" && fileErr != nil {
				t.Fatal(fileErr)
			}
		})
	}
}

const notificationSchedulerMock = `
$script:principal = [pscustomobject]@{UserId=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value;LogonType=3;RunLevel=0}
if ($script:scenario -eq 'other_user') { $script:principal.UserId = 'S-1-5-18' }
if ($script:scenario -eq 'named_user') { $script:principal.UserId = [Security.Principal.WindowsIdentity]::GetCurrent().Name }
$script:task = [pscustomobject]@{Enabled=($script:scenario -ne 'disabled');State=3;Definition=[pscustomobject]@{Principal=$script:principal;RegistrationInfo=[pscustomobject]@{Description=('EigenFlux notification watch '+('a'*32))}}}
if ($script:scenario -in @('running','named_user','upgrade')) { $script:task.State=4 }
$script:task | Add-Member ScriptMethod Stop { param($unused); $this.State=3 }
$script:task | Add-Member ScriptMethod GetInstances { param($unused); return [pscustomobject]@{Count=0} }
$script:instance = [pscustomobject]@{State=4}
$script:refreshCount=0
$script:instance | Add-Member ScriptMethod Refresh { $script:refreshCount++;if ($script:scenario -eq 'exited' -and $script:refreshCount -gt 1) { $this.State=3 } }
$script:task | Add-Member ScriptMethod Run { param($unused);return $script:instance }
$script:folder = [pscustomobject]@{}
$script:folder | Add-Member ScriptMethod GetTask { param($name);if ($script:scenario -eq 'new') { throw [IO.FileNotFoundException]::new() };return $script:task }
$script:folder | Add-Member ScriptMethod RegisterTaskDefinition {
    param($name,$definition,$flags,$user,$password,$logon,$sddl)
    if ($definition.Principal.UserId -ne $script:principal.UserId -or $definition.Principal.LogonType -ne 3 -or $definition.Principal.RunLevel -ne 0 -or $definition.Settings.Hidden -or $definition.Settings.ExecutionTimeLimit -ne 'PT0S' -or $definition.Settings.MultipleInstances -ne 2) { throw 'invalid task settings' }
    return $script:task
}
$script:service = [pscustomobject]@{}
$script:service | Add-Member ScriptMethod Connect {}
$script:service | Add-Member ScriptMethod GetFolder {param($path);return $script:folder}
$script:service | Add-Member ScriptMethod NewTask {
    param($unused)
    $definition = [pscustomobject]@{RegistrationInfo=[pscustomobject]@{Description=''};Principal=[pscustomobject]@{UserId='';LogonType=0;RunLevel=0};Settings=[pscustomobject]@{Enabled=$false;Hidden=$false;AllowDemandStart=$false;DisallowStartIfOnBatteries=$true;StopIfGoingOnBatteries=$true;ExecutionTimeLimit='';MultipleInstances=0;RestartInterval='';RestartCount=0;RunOnlyIfIdle=$true;StartWhenAvailable=$false;IdleSettings=[pscustomobject]@{StopOnIdleEnd=$true}};Triggers=[pscustomobject]@{};Actions=[pscustomobject]@{}}
    $definition.Triggers | Add-Member ScriptMethod Create {param($type);if($type -ne 9){throw 'not logon trigger'};return [pscustomobject]@{UserId=''}}
    $definition.Actions | Add-Member ScriptMethod Create {param($type);return [pscustomobject]@{Path='';Arguments='';WorkingDirectory=''}}
    return $definition
}
function New-Object {param([string]$ComObject);if($ComObject -ne 'Schedule.Service'){throw 'unexpected COM'};return $script:service}
function Start-Sleep {param($Milliseconds,$Seconds)}
`
