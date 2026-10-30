package watchservice

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestNotificationWrapperRestartsFailedNativeChild(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("Go toolchain is required to build the isolated native child")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "child.go")
	helper := filepath.Join(dir, "child.exe")
	// The child ignores the wrapper's CLI arguments and touches only this test directory.
	program := `package main
import("os";"path/filepath";"strconv";"strings";"time")
func main(){exe,_:=os.Executable();dir:=filepath.Dir(exe);path:=filepath.Join(dir,"count");data,_:=os.ReadFile(path);n,_:=strconv.Atoi(strings.TrimSpace(string(data)));n++;if os.WriteFile(path,[]byte(strconv.Itoa(n)),0600)!=nil{os.Exit(2)};if n==1{os.Exit(1)};if os.WriteFile(filepath.Join(dir,"child.pid"),[]byte(strconv.Itoa(os.Getpid())),0600)!=nil{os.Exit(2)};for{time.Sleep(time.Second)}}`
	if err = os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelBuild()
	build := exec.CommandContext(buildCtx, goTool, "build", "-o", helper, source)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build isolated child: %v\n%s", err, output)
	}
	wrapper, err := os.ReadFile("notification_watch_windows.ps1")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(wrapper), "Start-Sleep -Seconds 15", "Start-Sleep -Milliseconds 100")
	script := filepath.Join(dir, "parent.ps1")
	if err = os.WriteFile(script, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "watch.log")
	request := filepath.Join(dir, "request.json")
	payload, err := json.Marshal(map[string]any{"cli": helper, "home": dir, "server": "isolated-test", "agent_id": "test-agent", "principal_id": "test-principal", "log": log, "dispatch": true})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(request, payload, 0600); err != nil {
		t.Fatal(err)
	}
	ps := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, "-RequestFile", request)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("wrapper did not exit after cleanup")
		}
	}()
	var pid int
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			done <- err
			t.Fatalf("wrapper exited before restart: %v\n%s", err, output.String())
		default:
		}
		data, _ := os.ReadFile(filepath.Join(dir, "child.pid"))
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		if pid > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("wrapper failed to start its replacement native child")
	}
	count, err := os.ReadFile(filepath.Join(dir, "count"))
	if err != nil || strings.TrimSpace(string(count)) != "2" {
		t.Fatalf("expected exactly two native launches, got %q: %v", count, err)
	}
	lines, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range bytes.Split(lines, []byte("\n")) {
		var event struct {
			Type string `json:"type"`
			Data struct {
				ExitCode int `json:"exit_code"`
				Attempt  int `json:"attempt"`
			} `json:"data"`
		}
		if json.Unmarshal(line, &event) == nil && event.Type == "watch_process_exit" && event.Data.ExitCode == 1 && event.Data.Attempt == 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing first failure exit diagnostic: %s", lines)
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(child)
	state, err := windows.WaitForSingleObject(child, 0)
	if err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("replacement child is not running: %d %v", state, err)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	state, err = windows.WaitForSingleObject(child, 5000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("replacement child survived wrapper Job Object cleanup: %d %v", state, err)
	}
}
