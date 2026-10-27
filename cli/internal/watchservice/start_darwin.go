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

type notificationLaunchDefinition struct {
	Label            string
	Program          string
	ProgramArguments []string
	WorkingDirectory string
	Disabled         bool
}

func platformStartNotifications(ctx context.Context, task NotificationTask) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	home, err := os.UserHomeDir()
	if err != nil {
		return Result{}, err
	}
	return startNotificationLaunchAgent(ctx, task, home, os.Getuid(), func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).Output()
	})
}

// Existing launchers remain authoritative. Setup must never manufacture a second
// task or replace the command, environment, logs or account of an existing task.
func startNotificationLaunchAgent(ctx context.Context, task NotificationTask, home string, uid int, run launchRunner) (Result, error) {
	if uid <= 0 || !filepath.IsAbs(task.Home) || task.Server == "" || task.AgentID == "" || task.PrincipalID == "" || !task.Dispatch {
		return Result{}, errors.New("macOS notification setup requires a desktop user and pinned dispatch account")
	}
	files := []string{task.LaunchAgent}
	if task.LaunchAgent == "" {
		var err error
		files, err = filepath.Glob(filepath.Join(home, "Library", "LaunchAgents", "*.plist"))
		if err != nil {
			return Result{}, err
		}
	}
	var selected string
	var definition notificationLaunchDefinition
	for _, file := range files {
		d, err := notificationLaunchAgent(ctx, file, home, uid, task, run)
		if err != nil {
			if task.LaunchAgent != "" {
				return Result{}, err
			}
			continue
		}
		if selected != "" {
			return Result{}, errors.New("multiple account watch LaunchAgents found; select the existing task with --launch-agent")
		}
		selected, definition = file, d
	}
	if selected == "" {
		return Result{}, errors.New("no verifiable existing account watch LaunchAgent found; specify --launch-agent with its existing plist")
	}
	if definition.Disabled {
		return Result{}, errors.New("preserve disabled LaunchAgent; enable the intended task before setup")
	}
	domain := "gui/" + strconv.Itoa(uid)
	target := domain + "/" + definition.Label
	if _, err := run(ctx, "/bin/launchctl", "print", domain); err != nil {
		return Result{}, fmt.Errorf("desktop session unavailable: %w", err)
	}
	disabled, err := run(ctx, "/bin/launchctl", "print-disabled", domain)
	if err != nil {
		return Result{}, fmt.Errorf("read LaunchAgent enabled state: %w", err)
	}
	if regexp.MustCompile(`(?m)^\s*"` + regexp.QuoteMeta(definition.Label) + `"\s*=>\s*true\s*$`).Match(disabled) {
		return Result{}, errors.New("preserve LaunchAgent disabled in system settings")
	}
	raw, printErr := run(ctx, "/bin/launchctl", "print", target)
	pid := launchPID.FindSubmatch(raw)
	if len(pid) > 0 {
		if task.PrepareOnly {
			return Result{}, errors.New("existing watch process is active; wait for a safe maintenance point before setup")
		}
		return waitNotificationLaunchAgent(ctx, target, run)
	}
	if task.PrepareOnly {
		if printErr == nil {
			if _, err = run(ctx, "/bin/launchctl", "bootout", target); err != nil {
				return Result{}, fmt.Errorf("unload idle watch task: %w", err)
			}
		}
		return Result{Status: "task_stopped", Manager: "launchd", Target: target}, nil
	}
	if printErr != nil {
		if _, err = run(ctx, "/bin/launchctl", "bootstrap", domain, selected); err != nil {
			return Result{}, fmt.Errorf("load existing watch LaunchAgent: %w", err)
		}
	}
	if _, err = run(ctx, "/bin/launchctl", "kickstart", "-p", target); err != nil {
		return Result{}, fmt.Errorf("start existing watch LaunchAgent: %w", err)
	}
	return waitNotificationLaunchAgent(ctx, target, run)
}

func waitNotificationLaunchAgent(ctx context.Context, target string, run launchRunner) (Result, error) {
	stablePID, since := 0, time.Time{}
	for {
		raw, err := run(ctx, "/bin/launchctl", "print", target)
		if err != nil {
			return Result{}, fmt.Errorf("watch LaunchAgent disappeared: %w", err)
		}
		pid := 0
		if match := launchPID.FindSubmatch(raw); len(match) == 2 {
			pid, _ = strconv.Atoi(string(match[1]))
		}
		if pid > 1 && strings.Contains(string(raw), "\tstate = running\n") {
			if pid != stablePID {
				stablePID, since = pid, time.Now()
			}
			if time.Since(since) >= 3*time.Second {
				return Result{Status: "process_started", Manager: "launchd", Target: target, PID: pid}, nil
			}
		} else {
			stablePID, since = 0, time.Time{}
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Result{}, fmt.Errorf("watch LaunchAgent did not stay running; inspect its existing logs: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func ownedLaunchFile(file string, uid int) error {
	info, err := os.Lstat(file)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || !ok || int(stat.Uid) != uid {
		return errors.New("launcher must be a regular file owned by this user and not writable by others")
	}
	return nil
}

func notificationLaunchAgent(ctx context.Context, file, home string, uid int, task NotificationTask, run launchRunner) (notificationLaunchDefinition, error) {
	var d notificationLaunchDefinition
	if !filepath.IsAbs(file) || filepath.Dir(filepath.Clean(file)) != filepath.Join(home, "Library", "LaunchAgents") || filepath.Ext(file) != ".plist" {
		return d, errors.New("LaunchAgent must be directly inside this user's ~/Library/LaunchAgents")
	}
	if err := ownedLaunchFile(file, uid); err != nil {
		return d, err
	}
	raw, err := run(ctx, "/usr/bin/plutil", "-convert", "json", "-o", "-", file)
	if err != nil {
		return d, err
	}
	if json.Unmarshal(raw, &d) != nil || !launchLabel.MatchString(d.Label) {
		return d, errors.New("invalid LaunchAgent definition")
	}
	args := d.ProgramArguments
	if len(args) == 0 {
		return d, errors.New("LaunchAgent has no verifiable command")
	}
	if d.Program != "" && d.Program != args[0] {
		return d, errors.New("LaunchAgent Program differs from its command")
	}
	if d.WorkingDirectory != "" {
		info, err := os.Stat(d.WorkingDirectory)
		if !filepath.IsAbs(d.WorkingDirectory) || err != nil || !info.IsDir() {
			return d, errors.New("LaunchAgent working directory unavailable")
		}
	}
	if args[0] == "/bin/bash" || args[0] == "/bin/sh" || args[0] == "/bin/zsh" {
		if len(args) != 2 || !filepath.IsAbs(args[1]) {
			return d, errors.New("unsupported shell launcher")
		}
		if err := ownedLaunchFile(args[1], uid); err != nil {
			return d, err
		}
		raw, err = os.ReadFile(args[1])
		if err != nil {
			return d, err
		}
		args, err = notificationWrapperCommand(string(raw))
		if err != nil {
			return d, err
		}
	}
	if !notificationAccountCommand(args, task) {
		return d, errors.New("LaunchAgent does not launch this Home/server dispatch watch")
	}
	return d, nil
}

// Accept only a non-interpolating shell word grammar. Do not execute a wrapper
// to discover its account, and reject control flow or extra commands.
func notificationWrapperCommand(raw string) ([]string, error) {
	var command []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if command != nil {
			return nil, errors.New("commands after wrapper exec are unsupported")
		}
		if line == "set -eu" || line == "set -e" || line == "umask 077" {
			continue
		}
		if strings.HasPrefix(line, "export PATH=") && !strings.ContainsAny(line, "$`;&|<>()\\") {
			continue
		}
		if !strings.HasPrefix(line, "exec ") {
			return nil, errors.New("wrapper must directly exec the account watch")
		}
		var err error
		command, err = notificationShellWords(strings.TrimPrefix(line, "exec "))
		if err != nil {
			return nil, err
		}
	}
	if command == nil {
		return nil, errors.New("wrapper has no account watch exec")
	}
	return command, nil
}

func notificationShellWords(s string) ([]string, error) {
	if strings.ContainsAny(s, "$`;&|<>()\\\r") {
		return nil, errors.New("shell interpolation and operators are unsupported")
	}
	var words []string
	var word strings.Builder
	var quote rune
	active := false
	for _, r := range s {
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			active = true
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			active = true
			continue
		}
		if r == ' ' || r == '\t' {
			if active {
				words = append(words, word.String())
				word.Reset()
				active = false
			}
			continue
		}
		word.WriteRune(r)
		active = true
	}
	if quote != 0 {
		return nil, errors.New("unterminated shell quote")
	}
	if active {
		words = append(words, word.String())
	}
	return words, nil
}

func notificationAccountCommand(args []string, task NotificationTask) bool {
	if len(args) != 7 || !filepath.IsAbs(args[0]) || filepath.Base(args[0]) != "eigenflux" {
		return false
	}
	executable, err := os.Stat(args[0])
	if err != nil || !executable.Mode().IsRegular() || executable.Mode().Perm()&0111 == 0 {
		return false
	}
	return args[1] == "--homedir" && filepath.Clean(args[2]) == filepath.Clean(task.Home) && args[3] == "--server" && args[4] == task.Server && args[5] == "watch" && args[6] == "--dispatch"
}
