package desktopnotify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in native acceptance uses a fresh sender and separate icon/shortcut assets.
// It displays real notifications but never edits the installed sender's settings.
func TestWindowsFreshSenderIntegration(t *testing.T) {
	if os.Getenv("EIGENFLUX_TEST_NATIVE_NOTIFICATIONS") != "1" {
		t.Skip("set EIGENFLUX_TEST_NATIVE_NOTIFICATIONS=1 for visible native acceptance")
	}
	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	script := strings.ReplaceAll(windowsScript, "ai.eigenflux.notifications", "ai.eigenflux.notificationtest."+suffix)
	script = strings.ReplaceAll(script, "EigenFlux Notifications.lnk", "EigenFlux Notification Test "+suffix+".lnk")
	script = strings.ReplaceAll(script, `EigenFlux\Notifications`, `EigenFlux\NotificationTests\`+suffix)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(action string) (string, error) {
		message := windowsSetupMessage(SystemLanguage())
		if action == "show" {
			message.ID = "native-test:" + suffix
		}
		xml, tag, err := windowsToast(message)
		if err != nil {
			return "", err
		}
		input, err := json.Marshal(map[string]interface{}{"action": action, "executable": executable, "xml": xml, "tag": tag, "icon_png": windowsIconPNG})
		if err != nil {
			return "", err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
		command.Stdin = bytes.NewReader(input)
		output, err := command.Output()
		return strings.TrimSpace(string(output)), err
	}
	t.Cleanup(func() {
		output, err := invoke("remove")
		if err != nil || output != "accepted" {
			t.Errorf("native fixture cleanup: %q %v", output, err)
		}
	})
	if output, err := invoke("check"); err == nil || output != "setup_required" {
		t.Fatalf("fresh check: %q %v", output, err)
	}
	for _, action := range []string{"enable", "check", "enable", "show", "check"} {
		output, err := invoke(action)
		if err != nil || output != "accepted" {
			t.Fatalf("native %s: %q %v", action, output, err)
		}
	}
}

// Exercise the real helper's reflected getter and status gate with fake .NET
// notifiers. This never registers an app or shows a desktop notification.
func TestWindowsSettingGetterAndGate(t *testing.T) {
	start, end := strings.Index(windowsScript, "function Get-NotificationSetting("), strings.Index(windowsScript, "\ntry {")
	gateStart := strings.Index(windowsScript, "    $stage = 'setting_read'")
	gateEnd := strings.Index(windowsScript, "    if ($request.action -eq 'show')")
	if start < 0 || end <= start || gateStart < 0 || gateEnd <= gateStart {
		t.Fatal("helper getter/gate missing")
	}
	getter, gate := windowsScript[start:end], windowsScript[gateStart:gateEnd]
	for _, setting := range []string{"Enabled", "DisabledForApplication", "DisabledForUser", "DisabledByGroupPolicy", "DisabledByManifest", "null", "FutureSetting"} {
		t.Run(setting, func(t *testing.T) {
			value := `"` + setting + `"`
			if setting == "null" {
				value = "null"
			}
			script := "$ErrorActionPreference = 'Stop'\n" + getter + "\nAdd-Type -TypeDefinition 'public class MockNotifier { public object Setting { get { return " + value + "; } } }'\n$notifierType = [MockNotifier]\n$notifier = New-Object MockNotifier\n" + gate + "\n[Console]::Out.WriteLine('accepted')"
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
			output, err := command.CombinedOutput()
			want := "permission_denied"
			if setting == "Enabled" {
				want = "accepted"
			}
			if setting == "null" || setting == "FutureSetting" {
				want = "setting_unavailable"
			}
			if strings.TrimSpace(string(output)) != want || (err == nil) != (setting == "Enabled") {
				t.Fatalf("setting %s: output=%q error=%v", setting, output, err)
			}
		})
	}
}

func TestWindowsFirstNotificationState(t *testing.T) {
	start, end := strings.Index(windowsScript, "function Get-NotificationSetting("), strings.Index(windowsScript, "\ntry {")
	gateStart := strings.Index(windowsScript, "    $stage = 'setting_read'")
	gateEnd := strings.Index(windowsScript, "    if ($request.action -eq 'show')")
	if start < 0 || end <= start || gateStart < 0 || gateEnd <= gateStart {
		t.Fatal("helper getter/gate missing")
	}
	for _, tc := range []struct{ name, action, final, hresult, want string }{
		{"setup-first-use", "enable", "Enabled", "80070490", "accepted:1"},
		{"check-does-not-send", "check", "Enabled", "80070490", "setup_required"},
		{"show-can-prime", "show", "Enabled", "80070490", "accepted:0"},
		{"setup-denied-after-prime", "enable", "DisabledForUser", "80070490", "permission_denied"},
		{"setup-still-missing", "enable", "missing", "80070490", "setting_unavailable"},
		{"unrelated-error", "enable", "Enabled", "80004005", "failure:80004005"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := "$ErrorActionPreference='Stop'\n" + windowsScript[start:end] + `
Add-Type -TypeDefinition 'public class FirstNotifier {
 public string Next; public bool Initialized;
 public object Setting { get {
  if (!Initialized || Next == "missing") throw new System.Runtime.InteropServices.COMException("", unchecked((int)0x` + tc.hresult + `));
  return Next;
 } }
}'
$notifierType=[FirstNotifier]
$notifier=New-Object FirstNotifier
$notifier.Next='` + tc.final + `'
$request=@{action='` + tc.action + `'}
$script:sends=0
function Send-Notification($notifier,$request) { $script:sends++; $notifier.Initialized=$true }
try {
` + windowsScript[gateStart:gateEnd] + `
[Console]::Out.WriteLine(('accepted:'+ $script:sends))
} catch {
 $e=$_.Exception
 while($null -ne $e.InnerException){$e=$e.InnerException}
 [Console]::Out.WriteLine(('failure:'+ $e.HResult.ToString('X8')))
 exit 1
}`
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
			output, err := command.CombinedOutput()
			if strings.TrimSpace(string(output)) != tc.want || (err == nil) != strings.HasPrefix(tc.want, "accepted:") {
				t.Fatalf("output=%q error=%v; want %s", output, err, tc.want)
			}
		})
	}
}
