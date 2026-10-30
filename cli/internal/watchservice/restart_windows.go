package watchservice

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

//go:embed restart_windows.ps1
var windowsRestartScript string

func platformRestart(ctx context.Context, target Target) (Result, error) {
	if target.LaunchAgent != "" || strings.TrimSpace(target.ScheduledTask) == "" {
		return Result{}, errors.New("Windows watch restart requires --scheduled-task with an existing task name")
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		return Result{}, errors.New("Windows SystemRoot is unavailable")
	}
	input, err := json.Marshal(struct {
		TaskName string `json:"task_name"`
	}{target.ScheduledTask})
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	powershell := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	command := exec.CommandContext(ctx, powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", windowsRestartScript)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	command.Stdin = bytes.NewReader(input)
	output, runErr := command.Output()
	if ctx.Err() != nil {
		return Result{}, fmt.Errorf("Windows watch restart timed out or was cancelled; inspect the existing task in Task Scheduler before retrying: %w", ctx.Err())
	}
	var response struct {
		Status string `json:"status"`
		Target string `json:"target"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return Result{}, errors.New("Windows watch restart could not read Task Scheduler's response; check PowerShell policy and the existing task")
	}
	if message, ok := windowsRestartErrors[response.Error]; ok {
		return Result{}, errors.New(message)
	}
	if runErr != nil || response.Error != "" || response.Status != "process_started" || response.Target == "" {
		return Result{}, errors.New("Windows watch restart failed; inspect the existing task and its logs in Task Scheduler")
	}
	return Result{Status: "process_started", Manager: "task_scheduler", Target: response.Target, RuntimeVerified: false}, nil
}

var windowsRestartErrors = map[string]string{
	"invalid_task_name":     "provide the exact existing task name, including its Task Scheduler folder; wildcards and relative paths are not supported",
	"scheduler_access":      "cannot access Task Scheduler; run watch restart from the task owner's logged-in Windows session",
	"task_not_found":        "the specified task was not found or cannot be read; check its exact path and current-user permissions in Task Scheduler",
	"task_identity":         "the task must belong to the current Windows user; check its General settings in Task Scheduler",
	"interactive_required":  "the task must use 'Run only when user is logged on' for desktop notifications",
	"elevation_required":    "watch restart does not run elevated tasks; clear 'Run with highest privileges' in the task's General settings",
	"task_disabled":         "the existing task is disabled; enable it in Task Scheduler before retrying",
	"demand_start_disabled": "the task does not allow manual starts; enable 'Allow task to be run on demand' in Task Scheduler",
	"stop_failed":           "could not stop the existing task; inspect its running instances in Task Scheduler before retrying",
	"start_failed":          "could not start the existing task; check its action, working directory, permissions and logs in Task Scheduler",
	"process_exited":        "the restarted task exited before becoming stable; inspect the watch startup script and its logs",
	"start_timeout":         "the task did not enter a stable running state; inspect its conditions, action and logs in Task Scheduler",
}
