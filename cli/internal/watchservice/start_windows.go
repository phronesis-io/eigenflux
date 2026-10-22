package watchservice

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

//go:embed start_windows.ps1
var windowsStartScript string

//go:embed notification_watch_windows.ps1
var notificationWatchWrapper string

func platformStartNotifications(ctx context.Context, task NotificationTask) (Result, error) {
	if !filepath.IsAbs(task.CLI) || !filepath.IsAbs(task.Home) || task.Server == "" ||
		!regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(task.Scope) || task.AgentID == "" || task.PrincipalID == "" {
		return Result{}, errors.New("notification watch requires an absolute CLI/Home and pinned account")
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		return Result{}, errors.New("Windows SystemRoot is unavailable")
	}
	dir := filepath.Join(task.Home, "watch")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Result{}, err
	}
	name := "EigenFlux-Notifications-" + task.Scope
	wrapper := filepath.Join(dir, name+".ps1")
	requestFile := filepath.Join(dir, name+".json")
	request := struct {
		NotificationTask
		TaskName    string `json:"task_name"`
		Wrapper     string `json:"wrapper"`
		RequestFile string `json:"request_file"`
		Log         string `json:"log"`
	}{task, name, wrapper, requestFile, filepath.Join(dir, name+".log")}
	input, err := json.Marshal(request)
	if err != nil {
		return Result{}, err
	}
	// Validate task ownership/enabled state in the scheduler before changing
	// its launcher files. The script writes only the request's fixed owned paths.
	payload := struct {
		Request        json.RawMessage `json:"request"`
		WrapperContent string          `json:"wrapper_content"`
	}{input, notificationWatchWrapper}
	data, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	powershell := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	command := exec.CommandContext(ctx, powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", windowsStartScript)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	command.Stdin = bytes.NewReader(data)
	output, runErr := command.Output()
	var response struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if json.Unmarshal(output, &response) != nil || runErr != nil || (response.Status != "process_started" && !(task.PrepareOnly && response.Status == "task_stopped")) {
		return Result{}, errors.New("notification watch could not start (" + response.Error + "); inspect Task Scheduler and " + request.Log)
	}
	return Result{Status: response.Status, Manager: "task_scheduler", Target: "\\" + name, RuntimeVerified: false}, nil
}
