package desktopnotify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
