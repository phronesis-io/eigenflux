package consolev2

import (
	"fmt"
	"regexp"
	"time"
)

type outerLoopEnvironment struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Container string `json:"container"`
	WSL       string `json:"wsl"`
}

type schedulerReadbackObservation struct {
	ObservedAt   int64  `json:"observed_at"`
	State        string `json:"state"`
	TaskHash     string `json:"task_hash,omitempty"`
	ScheduleHash string `json:"schedule_hash,omitempty"`
}

type outerLoopObservation struct {
	Environment outerLoopEnvironment          `json:"environment"`
	Scheduler   *schedulerReadbackObservation `json:"scheduler,omitempty"`
}

var outerHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

func outerOneOf(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}

// Keep this bounded wire contract aligned with cli/internal/maintenance/outer_loop.go.
func validateOuterLoopObservation(e maintenanceEvent) error {
	o := e.OuterLoop
	if o == nil || e.Commission != nil || e.Notification != nil || e.Source != "" || e.Platform != "" || e.ReasonCategory != "" || e.RunningVersion != "" {
		return fmt.Errorf("invalid outer loop metadata")
	}
	if !outerOneOf(e.Trigger, "manual", "auto") || !outerOneOf(e.Mode, "", "skill", "plugin") || e.DurationMS < 0 || e.DurationMS > int64(24*time.Hour/time.Millisecond) {
		return fmt.Errorf("invalid outer loop category")
	}
	for _, s := range []string{e.Host, e.FromVersion, e.ToVersion} {
		if !maintenanceValue.MatchString(s) {
			return fmt.Errorf("invalid outer loop version or host")
		}
	}
	if !outerOneOf(e.ErrorCode, "", "unsupported_platform", "unsupported_host", "permission_denied", "timeout", "cancelled", "preflight_failed", "identity_failed", "skills_failed", "host_discovery_failed", "host_probe_failed", "task_prepare_failed", "binding_failed", "task_start_failed", "readback_invalid", "scheduler_api_unavailable", "scheduler_readback_denied", "scheduler_readback_failed", "user_disabled", "host_managed", "pending_auth") {
		return fmt.Errorf("invalid outer loop error code")
	}
	if (e.Result == "failed" || e.Result == "blocked") && e.ErrorCode == "" || outerOneOf(e.Result, "started", "installed", "verified") && e.ErrorCode != "" {
		return fmt.Errorf("invalid outer loop outcome")
	}
	env := o.Environment
	if !outerOneOf(env.OS, "linux", "windows", "darwin", "other") || !outerOneOf(env.Arch, "amd64", "arm64", "386", "arm", "ppc64", "ppc64le", "riscv64", "s390x", "loong64", "mips", "mipsle", "mips64", "mips64le", "wasm", "other") || !outerOneOf(env.Container, "detected", "not_detected", "unknown", "not_applicable") || !outerOneOf(env.WSL, "detected", "not_detected", "unknown", "not_applicable") {
		return fmt.Errorf("invalid outer loop environment")
	}
	if env.OS != "linux" && env.WSL != "not_applicable" {
		return fmt.Errorf("WSL detection requires a Linux process")
	}
	if e.Component == "watch" {
		if o.Scheduler != nil || e.ToVersion != "" || !outerOneOf(e.Phase, "preflight", "identity", "skills", "host_discovery", "host_probe", "task_prepare", "binding", "task_start") || !outerOneOf(e.Result, "started", "installed", "failed", "blocked", "skipped") {
			return fmt.Errorf("invalid watch setup observation")
		}
		return nil
	}
	if e.Component != "scheduler" || !outerOneOf(e.Phase, "readback", "migration") || !outerOneOf(e.Result, "started", "verified", "failed", "blocked") {
		return fmt.Errorf("invalid scheduler readback observation")
	}
	if e.Result != "verified" {
		if o.Scheduler != nil {
			return fmt.Errorf("unverified scheduler receipt")
		}
		return nil
	}
	s := o.Scheduler
	if s == nil || s.ObservedAt <= 0 || s.ObservedAt > e.EventAt+int64(30*time.Second/time.Millisecond) || s.ObservedAt < e.EventAt-int64(5*time.Minute/time.Millisecond) || !outerOneOf(s.State, "enabled", "paused", "disabled", "unknown", "missing") {
		return fmt.Errorf("invalid scheduler readback")
	}
	if s.State == "missing" {
		if s.TaskHash != "" || s.ScheduleHash != "" {
			return fmt.Errorf("missing scheduler cannot name a task")
		}
	} else if !outerHash.MatchString(s.TaskHash) || !outerHash.MatchString(s.ScheduleHash) {
		return fmt.Errorf("invalid scheduler hashes")
	}
	return nil
}
