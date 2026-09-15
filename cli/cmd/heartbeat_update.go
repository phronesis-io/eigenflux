package cmd

import (
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/selfupdate"
	"cli.eigenflux.ai/internal/skills"
	"context"
	"encoding/base64"
	"github.com/spf13/cobra"
	"os"
	"os/exec"
	"strings"
)

const updateReexecEnv = "EIGENFLUX_UPDATE_REEXEC"

func updateHeartbeatCLI(cmd *cobra.Command, cfg *config.Config, minimum string) (selfupdate.Result, bool, error) {
	serverName := activeServerName()
	meta := clientMetaForServerName(serverName)
	if os.Getenv(updateReexecEnv) == "1" {
		return selfupdate.Result{Status: "restarted", Version: version}, false, nil
	}
	if meta.Mode == "plugin" || !automaticMaintenanceEnabled(cfg, "auto_cli_update") {
		return selfupdate.Result{Status: "skipped", Version: version}, false, nil
	}
	path, err := os.Executable()
	if err != nil {
		return selfupdate.Result{Status: "failed", Version: version, Error: err.Error()}, false, nil
	}
	key, _ := base64.StdEncoding.DecodeString(skills.VerifyPublicKeyBase64)
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	r := selfupdate.Check(ctx, selfupdate.Options{Executable: path, Version: version, Minimum: minimum, CDN: cdnBase(), Key: key})
	if r.Status != "updated" {
		return r, false, nil
	}
	// Run once with original arguments and inherited identity/runtime environment.
	// Business failures are returned, never replayed with the previous binary.
	child := exec.CommandContext(ctx, r.Executable, heartbeatReexecArgs(os.Args[1:], config.HomeDir(), serverName)...)
	child.Env = heartbeatReexecEnvironment(os.Environ())
	child.Stdin, child.Stdout, child.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err = child.Run(); err != nil {
		return r, true, &updatedCLIError{err}
	}
	return r, true, nil
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

func heartbeatReexecEnvironment(env []string) []string {
	out := make([]string, 0, len(env)+2)
	for _, v := range env {
		if strings.HasPrefix(v, updateReexecEnv+"=") || strings.HasPrefix(v, "EIGENFLUX_HOME=") {
			continue
		}
		out = append(out, v)
	}
	return append(out, updateReexecEnv+"=1", "EIGENFLUX_HOME="+config.HomeDir())
}

type updatedCLIError struct{ error }

func (e *updatedCLIError) Unwrap() error { return e.error }
