// Package watchservice restarts an explicitly selected existing OS task.
package watchservice

import (
	"context"
	"errors"
	"time"
)

type Target struct {
	LaunchAgent   string
	ScheduledTask string
}

type Result struct {
	Status          string `json:"status"`
	Manager         string `json:"manager"`
	Target          string `json:"target"`
	PID             int    `json:"pid,omitempty"`
	RuntimeVerified bool   `json:"runtime_verified"`
}

// Restart preserves the selected task's command, environment and account.
// A running OS task does not prove that watch connected or an Agent can work.
func Restart(ctx context.Context, target Target) (Result, error) {
	if (target.LaunchAgent == "") == (target.ScheduledTask == "") {
		return Result{}, errors.New("specify exactly one existing watch task: --launch-agent <plist> on macOS or --scheduled-task <task-path> on Windows")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	return platformRestart(ctx, target)
}
