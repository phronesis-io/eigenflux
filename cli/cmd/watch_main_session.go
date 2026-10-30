package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/heartbeatmigration"
	watchstate "cli.eigenflux.ai/internal/watch"
	"github.com/spf13/cobra"
)

// The host supplies scheduler identity through its native API. CLI never
// searches task titles or reads a scheduler's private database.
func mainSessionTask(b dispatch.Binding, raw string) (heartbeatmigration.Task, error) {
	var empty heartbeatmigration.Task
	if b.MainSessionID != "" {
		return empty, errors.New("scheduler bootstrap cannot replace a main session; use explicit switching")
	}
	if len(raw) > 2<<20 {
		return empty, errors.New("scheduler inventory exceeds size limit")
	}
	var in heartbeatmigration.Inventory
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return empty, err
	}
	seen := map[string]bool{}
	tasks := make([]heartbeatmigration.Task, 0, len(in.Tasks))
	for _, task := range in.Tasks {
		if task.ID == "" || seen[task.ID] {
			return empty, errors.New("missing or duplicate scheduler task ID")
		}
		seen[task.ID] = true
		if task.Owner == "eigenflux" && task.Purpose == "heartbeat" && (task.Status == "PAUSED" || task.Status == "paused") {
			if !filepath.IsAbs(task.Home) || task.Server == "" {
				return empty, errors.New("paused heartbeat has ambiguous Home or server")
			}
			continue
		}
		tasks = append(tasks, task)
	}
	in.Tasks = tasks
	plan, err := heartbeatmigration.Build(in, b.Home, b.Server, "main-session-readback")
	if err != nil {
		return empty, err
	}
	if len(plan.Before.Tasks) != 1 {
		return empty, errors.New("no unique owned EigenFlux heartbeat; explicitly select the main session")
	}
	task := plan.Before.Tasks[0]
	if task.Status != "ACTIVE" && task.Status != "active" {
		return empty, errors.New("paused heartbeat cannot enable main session binding")
	}
	if task.ThreadID == "" {
		return empty, errors.New("heartbeat has no verified target thread")
	}
	return task, nil
}

func hasSchedulerInput(cmd *cobra.Command) bool {
	stdin, _ := cmd.Flags().GetBool("scheduler-stdin")
	return stdin || cmd.Flags().Changed("scheduler-json") || cmd.Flags().Changed("scheduler-file")
}

func readSchedulerInput(cmd *cobra.Command) (string, error) {
	stdin, _ := cmd.Flags().GetBool("scheduler-stdin")
	count := 0
	for _, selected := range []bool{stdin, cmd.Flags().Changed("scheduler-json"), cmd.Flags().Changed("scheduler-file")} {
		if selected {
			count++
		}
	}
	if count != 1 {
		return "", errors.New("select exactly one scheduler input: stdin, file, or JSON")
	}
	if cmd.Flags().Changed("scheduler-json") {
		return cmd.Flags().GetString("scheduler-json")
	}
	input := cmd.InOrStdin()
	if cmd.Flags().Changed("scheduler-file") {
		path, _ := cmd.Flags().GetString("scheduler-file")
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		input = f
	}
	raw, err := io.ReadAll(io.LimitReader(input, (2<<20)+1))
	if err != nil {
		return "", err
	}
	if len(raw) > 2<<20 {
		return "", errors.New("scheduler inventory exceeds size limit")
	}
	return string(raw), nil
}

type mainSessionTarget struct {
	SessionID string `json:"main_session_id"`
	TaskID    string `json:"task_id,omitempty"`
}

func schedulerMainSession(cmd *cobra.Command, b dispatch.Binding) (mainSessionTarget, error) {
	raw, err := readSchedulerInput(cmd)
	if err != nil {
		return mainSessionTarget{}, err
	}
	task, err := mainSessionTask(b, raw)
	if err != nil {
		return mainSessionTarget{}, err
	}
	preflight, _ := cmd.Flags().GetBool("preflight")
	if !preflight {
		expectedTask, _ := cmd.Flags().GetString("expected-task-id")
		expectedThread, _ := cmd.Flags().GetString("expected-thread-id")
		if expectedTask == "" || expectedThread == "" || expectedTask != task.ID || expectedThread != task.ThreadID {
			return mainSessionTarget{}, errors.New("scheduler target changed or preflight guards missing; read the host scheduler again")
		}
	}
	return mainSessionTarget{SessionID: task.ThreadID, TaskID: task.ID}, nil
}

func pinCodexHome(b *dispatch.Binding, explicit string) error {
	if b.Env == nil {
		b.Env = map[string]string{}
	}
	home := explicit
	if home == "" {
		home = b.Env["CODEX_HOME"]
	}
	if home == "" {
		home = os.Getenv("CODEX_HOME")
	}
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		home = filepath.Join(userHome, ".codex")
	}
	if !filepath.IsAbs(home) {
		return errors.New("Codex state directory must be absolute")
	}
	b.Env["CODEX_HOME"] = filepath.Clean(home)
	return nil
}

func prepareInitialMainSession(cmd *cobra.Command, b *dispatch.Binding) (mainSessionTarget, error) {
	target := mainSessionTarget{SessionID: b.MainSessionID}
	if b.Host != "codex" || (b.Mode != "" && b.Mode != "native") || (len(b.Events) > 0 && !b.Handles("pm_push")) {
		if hasSchedulerInput(cmd) {
			return target, errors.New("scheduler main sessions require native Codex PM dispatch")
		}
		return target, nil
	}
	if hasSchedulerInput(cmd) {
		if _, err := os.Stat(dispatch.BindingPath(b.Home, b.Server)); err == nil {
			return target, errors.New("existing Codex binding requires a session-only update")
		} else if !os.IsNotExist(err) {
			return target, err
		}
		var err error
		target, err = schedulerMainSession(cmd, *b)
		if err != nil {
			return target, err
		}
		b.MainSessionID = target.SessionID
	} else if b.MainSessionID == "" {
		return target, errors.New("main session required; supply a verified scheduler inventory or explicit main_session_id")
	}
	home, _ := cmd.Flags().GetString("codex-home")
	return target, pinCodexHome(b, home)
}

func selectMainSession(cmd *cobra.Command, b *dispatch.Binding) (mainSessionTarget, error) {
	var target mainSessionTarget
	id, _ := cmd.Flags().GetString("main-session-id")
	current, _ := cmd.Flags().GetBool("current-session")
	selected := 0
	for _, enabled := range []bool{cmd.Flags().Changed("main-session-id"), current, hasSchedulerInput(cmd)} {
		if enabled {
			selected++
		}
	}
	if selected != 1 {
		return target, errors.New("specify exactly one main session selector: explicit ID, current session, or scheduler input")
	}
	if b.Host != "codex" || b.Mode != "native" {
		if current || hasSchedulerInput(cmd) || cmd.Flags().Changed("codex-home") {
			return target, errors.New("ACP main sessions require an explicit owner session ID; Codex selectors do not apply")
		}
		return mainSessionTarget{SessionID: id}, nil
	}
	if current {
		id = os.Getenv("CODEX_THREAD_ID")
		if id == "" {
			return target, errors.New("calling Codex thread ID unavailable; specify --main-session-id")
		}
	}
	target.SessionID = id
	if hasSchedulerInput(cmd) {
		var err error
		target, err = schedulerMainSession(cmd, *b)
		if err != nil {
			return target, err
		}
	}
	home, _ := cmd.Flags().GetString("codex-home")
	return target, pinCodexHome(b, home)
}

func mainSessionPreflight(cmd *cobra.Command, target mainSessionTarget, previous, revision string) error {
	return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"status": "main_session_preflight", "task_id": target.TaskID, "main_session_id": target.SessionID, "expected_id": previous, "expected_revision": revision})
}

func lockMainSessionWrite(cmd *cobra.Command, b dispatch.Binding) (func(), error) {
	preflight, _ := cmd.Flags().GetBool("preflight")
	if preflight {
		return func() {}, nil
	}
	return watchstate.Acquire(b.Home, b.Server)
}

type mainSessionBindingPlan struct {
	Status           string `json:"status"`
	MainSessionID    string `json:"main_session_id"`
	ExpectedRevision string `json:"expected_revision"`
	Error            string `json:"error,omitempty"`
}

func inspectMainSessionBinding() (*mainSessionBindingPlan, error) {
	home, err := watchstate.CanonicalHome(config.HomeDir())
	if err != nil {
		return nil, err
	}
	b, err := dispatch.ReadBinding(home, activeServerName())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if b.Host != "codex" || b.Mode != "native" || !b.Handles("pm_push") {
		return nil, nil
	}
	expected, err := watchBindingIdentity()
	if err != nil {
		return nil, err
	}
	if !sameBindingIdentity(b, expected) {
		return nil, errWatchIdentity
	}
	p := &mainSessionBindingPlan{Status: "ready", MainSessionID: b.MainSessionID, ExpectedRevision: b.Revision}
	if b.MainSessionID == "" {
		p.Status = "bootstrap_required"
	} else if err := b.ValidateMainSession(); err != nil {
		p.Status, p.Error = "unavailable", fmt.Sprint(err)
	}
	return p, nil
}
