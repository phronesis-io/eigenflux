package watchservice

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestNotificationWrapperStopsNativeChildWithParent(t *testing.T) {
	wrapper, err := os.ReadFile("notification_watch_windows.ps1")
	if err != nil {
		t.Fatal(err)
	}
	prelude, _, found := strings.Cut(string(wrapper), "$request = Get-Content")
	if !found {
		t.Fatal("missing wrapper job prelude")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	script := filepath.Join(dir, "parent.ps1")
	prelude = strings.Replace(prelude, "param([Parameter(Mandatory=$true)][string]$RequestFile)", "param([string]$PIDFile)", 1)
	prelude += `
$child = Start-Process -FilePath "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" -ArgumentList @('-NoProfile','-Command','Start-Sleep -Seconds 60') -WindowStyle Hidden -PassThru
[IO.File]::WriteAllText($PIDFile, [string]$child.Id)
Start-Sleep -Seconds 60
`
	if err := os.WriteFile(script, []byte(prelude), 0600); err != nil {
		t.Fatal(err)
	}
	ps := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, "-PIDFile", pidFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	deadline := time.Now().Add(15 * time.Second)
	var pidText []byte
	for time.Now().Before(deadline) {
		pidText, err = os.ReadFile(pidFile)
		if err == nil && len(pidText) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidText)))
	if err != nil {
		t.Fatalf("guard did not start child: %v", err)
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(child)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	state, err := windows.WaitForSingleObject(child, 5000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("native watch child survived parent exit: %d %v", state, err)
	}
}
