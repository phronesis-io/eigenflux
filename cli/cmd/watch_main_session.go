package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
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
func mainSessionFromScheduler(b dispatch.Binding, raw string) (string, error) {
	if b.MainSessionID != "" {
		return "", errors.New("scheduler bootstrap cannot replace a main session; use explicit switching")
	}
	if len(raw) > 2<<20 {
		return "", errors.New("scheduler inventory exceeds size limit")
	}
	var in heartbeatmigration.Inventory
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return "", err
	}
	plan, err := heartbeatmigration.Build(in, b.Home, b.Server, "main-session-readback")
	if err != nil {
		return "", err
	}
	if len(plan.Before.Tasks) != 1 {
		return "", errors.New("no unique owned EigenFlux heartbeat; explicitly select the main session")
	}
	task := plan.Before.Tasks[0]
	if task.Status != "ACTIVE" && task.Status != "active" {
		return "", errors.New("paused heartbeat cannot enable main session binding")
	}
	if task.ThreadID == "" {
		return "", errors.New("heartbeat has no verified target thread")
	}
	return task.ThreadID, nil
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

func prepareInitialMainSession(b *dispatch.Binding) error {
	if b.Host != "codex" || (b.Mode != "" && b.Mode != "native") || (len(b.Events) > 0 && !b.Handles("pm_push")) {
		return nil
	}
	if b.MainSessionID == "" {
		if _, err := os.Stat(dispatch.BindingPath(b.Home, b.Server)); err == nil {
			return errors.New("existing Codex binding requires scheduler bootstrap or explicit main session switching")
		} else if !os.IsNotExist(err) {
			return err
		}
		b.MainSessionID = os.Getenv("CODEX_THREAD_ID")
		if b.MainSessionID == "" {
			return errors.New("main session unavailable; bind from the setup conversation or supply main_session_id")
		}
	}
	return pinCodexHome(b, "")
}

func selectMainSession(cmd *cobra.Command, b *dispatch.Binding) (string, error) {
	id, _ := cmd.Flags().GetString("main-session-id")
	current, _ := cmd.Flags().GetBool("current-session")
	raw, _ := cmd.Flags().GetString("scheduler-json")
	selected := 0
	for _, enabled := range []bool{cmd.Flags().Changed("main-session-id"), current, cmd.Flags().Changed("scheduler-json")} {
		if enabled {
			selected++
		}
	}
	if selected != 1 {
		return "", errors.New("specify exactly one of --main-session-id, --current-session, or --scheduler-json")
	}
	if current {
		id = os.Getenv("CODEX_THREAD_ID")
		if id == "" {
			return "", errors.New("calling Codex thread ID unavailable; specify --main-session-id")
		}
	}
	if cmd.Flags().Changed("scheduler-json") {
		var err error
		id, err = mainSessionFromScheduler(*b, raw)
		if err != nil {
			return "", err
		}
	}
	home, _ := cmd.Flags().GetString("codex-home")
	return id, pinCodexHome(b, home)
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
