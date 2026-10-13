package watchservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type launchRunner func(context.Context, string, ...string) ([]byte, error)

var launchLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$`)
var launchPID = regexp.MustCompile(`(?m)^\tpid = ([0-9]+)\s*$`)

func platformRestart(ctx context.Context, target Target) (Result, error) {
	if target.LaunchAgent == "" {
		return Result{}, errors.New("macOS watch restart requires --launch-agent <existing plist>")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Result{}, err
	}
	return restartLaunchAgent(ctx, target.LaunchAgent, home, os.Getuid(), func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).Output()
	}, 3*time.Second, 250*time.Millisecond)
}

func restartLaunchAgent(ctx context.Context, file, home string, uid int, run launchRunner, stableFor, interval time.Duration) (Result, error) {
	if uid <= 0 {
		return Result{}, errors.New("run watch restart as the signed-in desktop user, without sudo")
	}
	directory := filepath.Join(home, "Library", "LaunchAgents")
	if !filepath.IsAbs(file) || filepath.Dir(filepath.Clean(file)) != filepath.Clean(directory) || filepath.Ext(file) != ".plist" {
		return Result{}, errors.New("--launch-agent must name an existing plist directly inside your ~/Library/LaunchAgents directory")
	}
	info, err := os.Lstat(file)
	if err != nil {
		return Result{}, fmt.Errorf("read LaunchAgent plist: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || !ok || int(stat.Uid) != uid {
		return Result{}, errors.New("LaunchAgent plist must be a regular file owned by this user and not writable by other users")
	}
	raw, err := run(ctx, "/usr/bin/plutil", "-convert", "json", "-o", "-", file)
	if err != nil {
		return Result{}, fmt.Errorf("invalid LaunchAgent plist: %w", err)
	}
	var definition struct {
		Label            string
		Program          string
		ProgramArguments []string
		WorkingDirectory string
		Disabled         bool
	}
	if json.Unmarshal(raw, &definition) != nil || !launchLabel.MatchString(definition.Label) {
		return Result{}, errors.New("LaunchAgent plist has an invalid Label")
	}
	if definition.Disabled {
		return Result{}, errors.New("LaunchAgent is disabled in its plist; enable the intended watch task before restarting")
	}
	program := definition.Program
	if program == "" && len(definition.ProgramArguments) > 0 {
		program = definition.ProgramArguments[0]
	}
	executable, err := os.Stat(program)
	if !filepath.IsAbs(program) || err != nil || !executable.Mode().IsRegular() || executable.Mode().Perm()&0111 == 0 {
		return Result{}, errors.New("LaunchAgent Program must name an existing absolute executable; fix the task before restarting")
	}
	if definition.WorkingDirectory != "" {
		workdir, err := os.Stat(definition.WorkingDirectory)
		if !filepath.IsAbs(definition.WorkingDirectory) || err != nil || !workdir.IsDir() {
			return Result{}, errors.New("LaunchAgent WorkingDirectory is unavailable; fix the task before restarting")
		}
	}
	domain := "gui/" + strconv.Itoa(uid)
	target := domain + "/" + definition.Label
	// Verify a desktop session and disabled state before stopping a working task.
	if _, err := run(ctx, "/bin/launchctl", "print", domain); err != nil {
		return Result{}, fmt.Errorf("desktop session unavailable; run from the logged-in user's GUI session: %w", err)
	}
	disabled, err := run(ctx, "/bin/launchctl", "print-disabled", domain)
	if err != nil {
		return Result{}, fmt.Errorf("read LaunchAgent enabled state: %w", err)
	}
	pattern := `(?m)^\s*"` + regexp.QuoteMeta(definition.Label) + `"\s*=>\s*true\s*$`
	if regexp.MustCompile(pattern).Match(disabled) {
		return Result{}, errors.New("LaunchAgent is disabled in system settings; enable the intended watch task before restarting")
	}
	if _, err := run(ctx, "/bin/launchctl", "print", target); err == nil {
		if _, err := run(ctx, "/bin/launchctl", "bootout", target); err != nil {
			return Result{}, fmt.Errorf("stop existing watch LaunchAgent: %w", err)
		}
	}
	// Reload even an already-loaded task so modified plist arguments take effect.
	if _, err := run(ctx, "/bin/launchctl", "bootstrap", domain, file); err != nil {
		return Result{}, fmt.Errorf("load watch LaunchAgent failed; the task may be stopped; fix its plist and rerun watch restart: %w", err)
	}
	if _, err := run(ctx, "/bin/launchctl", "kickstart", "-p", target); err != nil {
		return Result{}, fmt.Errorf("start watch LaunchAgent failed; inspect its StandardErrorPath and rerun watch restart: %w", err)
	}
	// RunAtLoad may start the process before kickstart. Do not kill it a second time.
	stablePID, since := 0, time.Time{}
	for {
		raw, err := run(ctx, "/bin/launchctl", "print", target)
		if err != nil {
			return Result{}, fmt.Errorf("watch LaunchAgent disappeared after start; inspect its StandardErrorPath: %w", err)
		}
		pid := 0
		if match := launchPID.FindSubmatch(raw); len(match) == 2 {
			pid, _ = strconv.Atoi(string(match[1]))
		}
		if pid > 1 && strings.Contains(string(raw), "\tstate = running\n") {
			if pid != stablePID {
				stablePID, since = pid, time.Now()
			}
			if time.Since(since) >= stableFor {
				return Result{Status: "process_started", Manager: "launchd", Target: target, PID: pid}, nil
			}
		} else {
			stablePID, since = 0, time.Time{}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Result{}, fmt.Errorf("watch LaunchAgent did not stay running; inspect its StandardErrorPath and test-source startup: %w", ctx.Err())
		case <-timer.C:
		}
	}
}
