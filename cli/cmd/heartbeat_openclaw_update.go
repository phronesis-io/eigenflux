package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/pluginupdate"
	watchstate "cli.eigenflux.ai/internal/watch"
	"github.com/spf13/cobra"
)

func openClawMaintenancePlan(p pluginMaintenance) pluginMaintenance {
	return openClawMaintenancePlanAt(p, activeServerName(), time.Now())
}

func openClawMaintenancePlanAt(p pluginMaintenance, server string, now time.Time) pluginMaintenance {
	var saved pluginMaintenance
	b, err := os.ReadFile(maintenancePathForServer("plugin-openclaw-native", server))
	if err != nil || json.Unmarshal(b, &saved) != nil || saved.Context != p.Context {
		return p
	}
	age := now.Sub(saved.CheckedAt)
	if age < 0 {
		return p
	}
	// Never present historical loaded versions as current-process evidence.
	if saved.Status == "updating" && age < 5*time.Minute {
		p.Status, p.Due = "updating", false
	} else if (saved.Status == "loaded" || saved.Status == "not_installed") && age < 24*time.Hour {
		p.Status, p.Due = "not_due", false
	} else if age < 5*time.Minute {
		p.Status, p.Due, p.Error = saved.Status, false, saved.Error
	}
	p.Scope, p.LatestVersion, p.CheckedAt = saved.Scope, saved.LatestVersion, saved.CheckedAt
	p.AttemptID = saved.AttemptID
	p.RestartPendingVersion = saved.RestartPendingVersion
	return p
}

// Launch outside the Gateway's process group; installing a plugin may restart its caller.
var launchOpenClawUpdate = func(p pluginMaintenance) error {
	path, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"--homedir", config.HomeDir(), "--server", activeServerName(), "heartbeat", "plugin-update", "--worker"}
	if mode := clientMetaForServerName(activeServerName()).Mode; mode == "plugin" || mode == "skill" {
		args = append(args, "--runtime-mode", mode)
	}
	child := exec.Command(path, args...)
	child.Env = cleanPluginUpdateEnvironment(os.Environ())
	child.Dir = p.Context
	log, err := os.OpenFile(maintenancePath("plugin-openclaw-worker")+".log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	child.Stdout, child.Stderr = log, log
	detachPluginWorker(child)
	if err := child.Start(); err != nil {
		return err
	}
	return child.Process.Release()
}

func cleanPluginUpdateEnvironment(env []string) []string {
	out := make([]string, 0, len(env))
	for _, value := range env {
		skip := false
		for _, key := range []string{updateReexecEnv, updateAttemptEnv, updateIdentityEnv, updateFromVersionEnv, watchSupervisedEnv, watchAgentEnv, watchPrincipalEnv} {
			if strings.HasPrefix(value, key+"=") {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, value)
		}
	}
	return out
}

func scheduleOpenClawUpdate(p pluginMaintenance) pluginMaintenance {
	if p.Executor != "cli" || !p.Due {
		return p
	}
	path := maintenancePath("plugin-openclaw-native")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		p.Status, p.Error = "failed", err.Error()
		return p
	}
	release, err := watchstate.AcquirePath(path + ".launch.lock")
	if err != nil {
		p.Status, p.Due = "updating", false
		return p
	}
	defer release()
	if current := openClawMaintenancePlan(p); !current.Due {
		return current
	}
	p.Status, p.Due, p.CheckedAt = "updating", false, time.Now()
	recorder := newMaintenanceRecorder(activeServerName(), "plugin", "auto", "", os.Stderr)
	p.AttemptID = recorder.attempt
	recorder.Phase("check", "", "")
	if err := saveMaintenance(path, p); err != nil {
		p.Status, p.Error = "failed", err.Error()
		recorder.Finish("check", "failed", "", "", "", "worker_state_failed")
		recorder.Flush()
		return p
	}
	if err := launchOpenClawUpdate(p); err != nil {
		p.Status, p.Error = "failed", fmt.Sprintf("start plugin update worker: %v", err)
		_ = saveMaintenance(path, p)
		recorder.Finish("check", "failed", "", "", "", "worker_start_failed")
		recorder.Flush()
	}
	return p
}

func runOpenClawUpdate(ctx context.Context) (pluginMaintenance, error) {
	p := pluginMaintenance{Executor: "cli", Host: "openclaw", PluginID: pluginupdate.PluginID, Context: pluginContext(), AttemptID: maintenance.NewID()}
	cfg, err := config.Load()
	if err != nil {
		return p, err
	}
	meta := clientMetaForServerName(activeServerName())
	if runtimeProduct(meta.Host) != "openclaw" || !heartbeatMaintenanceEnabled(meta.Mode, cfg, "auto_plugin_update") {
		p.Status = "not_applicable"
		return p, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	bin := os.Getenv("EIGENFLUX_OPENCLAW_BIN")
	if bin == "" {
		bin = "openclaw"
	}
	server := activeServerName()
	recorder := newMaintenanceRecorder(server, "plugin", "auto", "", os.Stderr)
	saved := openClawMaintenancePlan(p)
	if recorder.authenticated && saved.AttemptID != "" {
		if previous, err := maintenance.LastAttempt(recorder.scope, "plugin"); err == nil && previous.AttemptID == saved.AttemptID && previous.Phase == "check" && previous.Result == "started" {
			recorder.attempt = saved.AttemptID
		}
	}
	p.AttemptID = recorder.attempt
	fromVersion := ""
	recordProgress := func(progress pluginupdate.Progress) {
		if progress.FromVersion != "" {
			fromVersion = progress.FromVersion
		}
		if progress.Result == "started" {
			recorder.Phase(progress.Phase, progress.FromVersion, progress.TargetVersion)
		} else {
			recorder.Finish(progress.Phase, progress.Result, progress.FromVersion, progress.TargetVersion, "", "")
		}
	}
	r := pluginupdate.Update(ctx, pluginupdate.Options{OnProgress: recordProgress, Server: server, CLIVersion: version, RestartPendingVersion: saved.RestartPendingVersion, Run: func(ctx context.Context, args ...string) ([]byte, error) {
		// Keep manager credentials in its own configuration; do not capture stderr in receipts.
		cmd := exec.CommandContext(ctx, bin, args...)
		return cmd.Output()
	}})
	p.Status, p.Scope, p.Error = r.Status, r.Scope, r.Error
	p.InstalledVersion, p.LatestVersion, p.LoadedVersion = r.InstalledVersion, r.LatestVersion, r.LoadedVersion
	p.RestartPendingVersion = r.RestartPendingVersion
	p.CheckedAt = time.Now()
	if fromVersion == "" {
		fromVersion = r.InstalledVersion
	}
	errorCode := ""
	if p.Status == "failed" || p.Status == "blocked" {
		errorCode = "host_maintenance_failed"
	}
	recorder.Finish(r.Phase, p.Status, fromVersion, p.LatestVersion, p.LoadedVersion, errorCode)
	saveErr := saveMaintenance(maintenancePath("plugin-openclaw-native"), p)
	recorder.Flush()
	if saveErr != nil {
		return p, saveErr
	}
	return p, nil
}

func init() {
	worker := false
	c := &cobra.Command{Use: "plugin-update", Hidden: true, Short: "Run the independent OpenClaw plugin maintenance worker", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !worker {
				return fmt.Errorf("plugin-update is an internal heartbeat worker")
			}
			p, err := runOpenClawUpdate(cmd.Context())
			if err != nil {
				return err
			}
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(p); err != nil {
				return err
			}
			if p.Status == "failed" || p.Status == "blocked" || p.Status == "restart_required" {
				return fmt.Errorf("plugin maintenance %s: %s", p.Status, p.Error)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&worker, "worker", false, "run as the independent maintenance worker")
	_ = c.Flags().MarkHidden("worker")
	heartbeatCmd.AddCommand(c)
}
