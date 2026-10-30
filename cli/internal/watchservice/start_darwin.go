package watchservice

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
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

var errExistingAccountLauncher = errors.New("existing account launcher requires repair")

type notificationLaunchDefinition struct {
	Label                string
	Program              string
	ProgramArguments     []string
	WorkingDirectory     string
	EnvironmentVariables map[string]string
	Disabled             bool
	watchArguments       []string
	wrapperPath          string
	wrapperSource        string
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

// Existing verifiable account launchers remain authoritative. Only a missing
// account launcher is created; existing commands and explicit disablement survive.
func startNotificationLaunchAgent(ctx context.Context, task NotificationTask, home string, uid int, run launchRunner) (Result, error) {
	if uid <= 0 || !filepath.IsAbs(task.Home) || task.Server == "" || task.AgentID == "" || task.PrincipalID == "" {
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
	create := false
	for _, file := range files {
		d, err := notificationLaunchAgent(ctx, file, home, uid, task, run)
		if err != nil {
			if task.LaunchAgent != "" || errors.Is(err, errExistingAccountLauncher) {
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
		if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(task.Scope) || !filepath.IsAbs(task.CLI) || filepath.Base(task.CLI) != "eigenflux" {
			return Result{}, errors.New("new watch LaunchAgent requires stable absolute CLI and account scope")
		}
		selected = filepath.Join(home, "Library", "LaunchAgents", "ai.eigenflux.watch."+task.Scope+".plist")
		if _, err := os.Lstat(selected); err == nil || !os.IsNotExist(err) {
			return Result{}, errors.New("existing account launcher could not be verified; preserve it")
		}
		definition = notificationLaunchDefinition{Label: "ai.eigenflux.watch." + task.Scope}
		create = true
	}
	if definition.Disabled {
		return Result{Status: "skipped_disabled", Manager: "launchd", Target: definition.Label}, nil
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
		return Result{Status: "skipped_disabled", Manager: "launchd", Target: target}, nil
	}
	raw, printErr := run(ctx, "/bin/launchctl", "print", target)
	if create && printErr == nil {
		return Result{}, errors.New("watch task exists without a verifiable owned launcher; preserve it")
	}
	pid := launchPID.FindSubmatch(raw)
	if len(pid) > 0 {
		if notificationModeChanged(definition, task) && task.Dispatch {
			// Notification-only watch cannot admit Agent work. Its verified
			// account service can stop before binding is promoted to dispatch.
			if _, err = run(ctx, "/bin/launchctl", "bootout", target); err != nil {
				return Result{}, fmt.Errorf("unload notification-only watch: %w", err)
			}
			printErr = errors.New("notification-only owner unloaded")
		} else {
			if task.PrepareOnly || notificationModeChanged(definition, task) {
				return Result{}, errors.New("existing watch process is active; wait for a safe maintenance point before setup")
			}
			return waitNotificationLaunchAgent(ctx, target, run)
		}
	}
	if task.PrepareOnly {
		if printErr == nil {
			if _, err = run(ctx, "/bin/launchctl", "bootout", target); err != nil {
				return Result{}, fmt.Errorf("unload idle watch task: %w", err)
			}
		}
		return Result{Status: "task_stopped", Manager: "launchd", Target: target}, nil
	}
	if !create && notificationModeChanged(definition, task) {
		if printErr == nil {
			if _, err = run(ctx, "/bin/launchctl", "bootout", target); err != nil {
				return Result{}, fmt.Errorf("unload watch task before mode change: %w", err)
			}
		}
		if err := changeNotificationLaunchMode(ctx, selected, definition, task, uid, run); err != nil {
			return Result{}, err
		}
		printErr = errors.New("reload changed watch mode")
	}
	if create {
		if err := writeNotificationLaunchAgent(selected, task, definition.Label); err != nil {
			return Result{}, err
		}
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
		d.wrapperPath, d.wrapperSource = args[1], string(raw)
		args, err = notificationWrapperCommand(string(raw))
		if err != nil {
			// An explicit foreign identity pin may exclude an opaque launcher,
			// but never bypass repair of this account's scope-named task.
			agent, principal := d.EnvironmentVariables["EIGENFLUX_WATCH_EXPECTED_AGENT"], d.EnvironmentVariables["EIGENFLUX_WATCH_EXPECTED_PRINCIPAL"]
			if d.Label != "ai.eigenflux.watch."+task.Scope && agent != "" && principal != "" && (agent != task.AgentID || principal != task.PrincipalID) {
				return d, errors.New("wrapper belongs to another pinned account")
			}
			if strings.HasPrefix(d.Label, "ai.eigenflux.") {
				return d, fmt.Errorf("%w: existing EigenFlux wrapper cannot be safely verified", errExistingAccountLauncher)
			}
			return d, err
		}
	}
	if !notificationAccountCommand(args, task) {
		return d, errors.New("LaunchAgent does not launch this Home/server dispatch watch")
	}
	if agent := d.EnvironmentVariables["EIGENFLUX_WATCH_EXPECTED_AGENT"]; agent != "" && (agent != task.AgentID || d.EnvironmentVariables["EIGENFLUX_WATCH_EXPECTED_PRINCIPAL"] != task.PrincipalID) {
		return d, fmt.Errorf("%w: launcher is pinned to another Agent", errExistingAccountLauncher)
	}
	if task.CLI != "" {
		actual, err := filepath.EvalSymlinks(args[0])
		intended, intendedErr := filepath.EvalSymlinks(task.CLI)
		if err != nil || intendedErr != nil || actual != intended {
			return d, fmt.Errorf("%w: existing account LaunchAgent uses a different CLI; update its launcher before recovery", errExistingAccountLauncher)
		}
	}
	d.watchArguments = args
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
	return args[1] == "--homedir" && filepath.Clean(args[2]) == filepath.Clean(task.Home) && args[3] == "--server" && args[4] == task.Server && args[5] == "watch" && (args[6] == "--notifications-only" || args[6] == "--dispatch")
}

func notificationModeChanged(d notificationLaunchDefinition, task NotificationTask) bool {
	return len(d.watchArguments) == 7 && (d.watchArguments[6] == "--dispatch") != task.Dispatch
}

func changeNotificationLaunchMode(ctx context.Context, file string, d notificationLaunchDefinition, task NotificationTask, uid int, run launchRunner) error {
	if err := ownedLaunchFile(file, uid); err != nil {
		return err
	}
	mode := "--notifications-only"
	if task.Dispatch {
		mode = "--dispatch"
	}
	if d.wrapperPath == "" {
		args := append([]string(nil), d.watchArguments...)
		args[6] = mode
		raw, _ := json.Marshal(args)
		if _, err := run(ctx, "/usr/bin/plutil", "-replace", "ProgramArguments", "-json", string(raw), file); err != nil {
			return fmt.Errorf("update owned watch mode: %w", err)
		}
		return nil
	}
	if err := ownedLaunchFile(d.wrapperPath, uid); err != nil {
		return err
	}
	raw, err := os.ReadFile(d.wrapperPath)
	if err != nil || string(raw) != d.wrapperSource {
		return errors.New("watch wrapper changed during mode update; retry")
	}
	// The verified wrapper has exactly one literal exec command. Change its
	// final mode token only, preserving environment setup and the original path.
	lines := strings.Split(d.wrapperSource, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "exec ") {
			at := strings.LastIndex(line, d.watchArguments[6])
			if at < 0 {
				return errors.New("watch wrapper mode unavailable")
			}
			lines[i] = line[:at] + mode + line[at+len(d.watchArguments[6]):]
		}
	}
	updated := strings.Join(lines, "\n")
	args, err := notificationWrapperCommand(updated)
	if err != nil || !notificationAccountCommand(args, task) || args[6] != mode {
		return errors.New("updated watch wrapper cannot be verified")
	}
	info, err := os.Stat(d.wrapperPath)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(d.wrapperPath), ".eigenflux-watch-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(info.Mode().Perm()); err == nil {
		_, err = tmp.WriteString(updated)
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp.Name(), d.wrapperPath)
}

func writeNotificationLaunchAgent(file string, task NotificationTask, label string) error {
	args := []string{task.CLI, "--homedir", task.Home, "--server", task.Server, "watch"}
	if task.Dispatch {
		args = append(args, "--dispatch")
	} else {
		args = append(args, "--notifications-only")
	}
	if !notificationAccountCommand(args, task) {
		return errors.New("watch executable is unavailable")
	}
	logs := filepath.Join(task.Home, "watch")
	if err := os.MkdirAll(logs, 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	escape := func(value string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(value))
		return b.String()
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>` + escape(label) + `</string><key>ProgramArguments</key><array>`)
	for _, arg := range args {
		b.WriteString("<string>" + escape(arg) + "</string>")
	}
	b.WriteString(`</array><key>EnvironmentVariables</key><dict><key>EIGENFLUX_WATCH_EXPECTED_AGENT</key><string>` + escape(task.AgentID) + `</string><key>EIGENFLUX_WATCH_EXPECTED_PRINCIPAL</key><string>` + escape(task.PrincipalID) + `</string></dict><key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>15</integer><key>StandardOutPath</key><string>` + escape(filepath.Join(logs, label+".out.log")) + `</string><key>StandardErrorPath</key><string>` + escape(filepath.Join(logs, label+".err.log")) + `</string></dict></plist>`)
	f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(b.String())
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
