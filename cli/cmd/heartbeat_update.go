package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/selfupdate"
	"cli.eigenflux.ai/internal/skills"
	"github.com/spf13/cobra"
)

const updateReexecEnv = "EIGENFLUX_UPDATE_REEXEC"
const updateAttemptEnv = "EIGENFLUX_UPDATE_ATTEMPT"
const updateIdentityEnv = "EIGENFLUX_UPDATE_IDENTITY"

// The process handoff is pinned independently of optional maintenance receipts.
type cliUpdateIdentity struct {
	Scope       maintenance.Scope `json:"scope"`
	PrincipalID string            `json:"principal_id"`
	Endpoint    string            `json:"endpoint"`
}

func captureCLIUpdateIdentity(server string) (*cliUpdateIdentity, error) {
	credentials, err := auth.LoadV2Credentials(server)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	target, err := cfg.GetActive(server)
	if err != nil {
		return nil, err
	}
	home, err := filepath.EvalSymlinks(config.HomeDir())
	if err != nil {
		return nil, err
	}
	return &cliUpdateIdentity{Scope: maintenance.Scope{Home: home, Server: target.Name, AgentID: credentials.AgentID}, PrincipalID: credentials.PrincipalID, Endpoint: target.Endpoint}, nil
}

func (identity *cliUpdateIdentity) verify() error {
	if identity == nil {
		return nil
	}
	if _, err := maintenanceIdentity(identity.Scope, identity.PrincipalID, identity.Endpoint); err != nil {
		return fmt.Errorf("CLI update identity changed; rerun explicitly: %w", err)
	}
	return nil
}

func verifyCLIUpdateReexecIdentity() error {
	encoded := os.Getenv(updateIdentityEnv)
	if os.Getenv(updateReexecEnv) != "1" || encoded == "" {
		return nil
	}
	var identity cliUpdateIdentity
	if err := json.Unmarshal([]byte(encoded), &identity); err != nil || identity.Scope.Home == "" || identity.Scope.Server == "" || identity.Scope.AgentID == "" || identity.Endpoint == "" {
		return fmt.Errorf("invalid CLI update identity handoff")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	target, err := cfg.GetActive(serverFlag)
	if err != nil || target.Name != identity.Scope.Server {
		return fmt.Errorf("CLI update server changed; rerun explicitly")
	}
	return identity.verify()
}

func updateHeartbeatCLI(cmd *cobra.Command, cfg *config.Config, minimum string) (selfupdate.Result, bool, error) {
	result, restarted, recorder, err := runObservedCLIUpdate(cmd, cfg, minimum, false)
	recorder.Flush()
	return result, restarted, err
}

func runObservedCLIUpdate(cmd *cobra.Command, cfg *config.Config, minimum string, manual bool) (selfupdate.Result, bool, *maintenanceRecorder, error) {
	server := serverFlag
	if cfg != nil {
		if target, err := cfg.GetActive(server); err == nil {
			server = target.Name
		}
	}
	trigger := "auto"
	if manual {
		trigger = "manual"
	}
	if os.Getenv(updateReexecEnv) == "1" {
		recorder := resumedCLIRecorder(server, trigger, cmd.ErrOrStderr())
		return selfupdate.Result{Status: "restarted", Version: version}, false, recorder, nil
	}
	recorder := newMaintenanceRecorder(server, "cli", trigger, "", cmd.ErrOrStderr())
	if !manual && (cfg == nil || !heartbeatMaintenanceEnabled(clientMetaForServerName(server).Mode, cfg, "auto_cli_update")) {
		return selfupdate.Result{Status: "skipped", Version: version}, false, recorder, nil
	}
	identity, err := captureCLIUpdateIdentity(server)
	if err != nil {
		if !manual {
			return selfupdate.Result{Status: "failed", Version: version, Error: err.Error()}, false, recorder, err
		}
		recorder.failure("update_identity_unavailable")
	}
	path, err := os.Executable()
	if err != nil {
		result := selfupdate.Result{Status: "failed", Version: version, Error: err.Error()}
		recorder.Complete(result, version)
		return result, false, recorder, nil
	}
	key, _ := base64.StdEncoding.DecodeString(skills.VerifyPublicKeyBase64)
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	updateCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	result := selfupdate.Check(updateCtx, selfupdate.Options{
		Home: config.HomeDir(), Executable: path, Version: version, Minimum: minimum,
		CDN: cdnBase(), Key: key, CheckNow: manual, OnProgress: recorder.Progress(version),
	})
	recorder.Complete(result, version)
	// Installation and synchronous rollback finish before restoring signal defaults.
	cancelled := updateCtx.Err()
	stop()
	if cancelled != nil {
		return result, false, recorder, cancelled
	}
	if err := identity.verify(); err != nil {
		return result, false, recorder, err
	}
	if result.Status != "updated" {
		return result, false, recorder, nil
	}
	recorder.Phase("execute", version, result.Version)
	child := exec.CommandContext(ctx, result.Executable, heartbeatReexecArgs(os.Args[1:], config.HomeDir(), server)...)
	child.Env = append(heartbeatReexecEnvironment(os.Environ()), updateAttemptEnv+"="+recorder.attempt, updateFromVersionEnv+"="+version)
	if identity != nil {
		encoded, _ := json.Marshal(identity)
		child.Env = append(child.Env, updateIdentityEnv+"="+string(encoded))
	}

	child.Stdin, child.Stdout, child.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := runHeartbeatReexec(child); err != nil {
		if child.Process == nil {
			recorder.Finish("execute", "failed", version, result.Version, "", "process_start_failed")
		}
		return result, true, recorder, &updatedCLIError{err}
	}
	return result, true, recorder, nil
}

// Handle termination only while the replacement CLI runs. SIGKILL cannot be
// intercepted; recovery from a killed parent is outside this waiter's scope.
func runHeartbeatReexec(child *exec.Cmd) error {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	if err := child.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	select {
	case err := <-done:
		return err
	case sig := <-signals:
		// Windows does not support forwarding these signals through os.Process.
		if err := child.Process.Signal(sig); err != nil {
			_ = child.Process.Kill()
		}
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-signals:
	case <-timer.C:
	}
	_ = child.Process.Kill()
	return <-done
}

func heartbeatReexecArgs(args []string, home, server string) []string {
	out := append([]string(nil), args...)
	if len(out) > 0 && out[len(out)-1] == "--" {
		out = out[:len(out)-1]
	}
	out = append(out, "--homedir", home)
	if server != "" {
		out = append(out, "--server", server)
	}
	return out
}

func automaticMaintenanceEnabled(cfg *config.Config, key string) bool {
	if value, found, err := cfg.GetServerKV(activeServerName(), key); err == nil && found {
		return value != "false"
	}
	return cfg.GetKV(key) != "false"
}

// Both host adapters and native triggers enter the same heartbeat plan pipeline.
// Unknown integration modes remain unknown rather than enabling maintenance.
func heartbeatMaintenanceEnabled(mode string, cfg *config.Config, key string) bool {
	return (mode == "skill" || mode == "plugin") && automaticMaintenanceEnabled(cfg, key)
}

func heartbeatReexecEnvironment(env []string) []string {
	out := make([]string, 0, len(env)+2)
	for _, v := range env {
		if strings.HasPrefix(v, updateReexecEnv+"=") || strings.HasPrefix(v, updateAttemptEnv+"=") || strings.HasPrefix(v, updateIdentityEnv+"=") || strings.HasPrefix(v, updateFromVersionEnv+"=") || strings.HasPrefix(v, "EIGENFLUX_HOME=") {
			continue
		}
		out = append(out, v)
	}
	return append(out, updateReexecEnv+"=1", "EIGENFLUX_HOME="+config.HomeDir())
}

type updatedCLIError struct{ error }

func (e *updatedCLIError) Unwrap() error { return e.error }
