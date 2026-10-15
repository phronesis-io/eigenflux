package cmd

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"cli.eigenflux.ai/internal/selfupdate"
	"cli.eigenflux.ai/internal/skills"
	"github.com/spf13/cobra"
)

type cliUpdateOutput struct {
	CLIUpdate  selfupdate.Result `json:"cli_update"`
	CLIVersion string            `json:"cli_version"`
	PID        int               `json:"pid"`
	Home       string            `json:"home"`
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check and install the latest signed CLI now",
	Long:  "Immediately check the signed CLI release, preserving signature, checksum and rollback protection. No Agent login is required. Automatic-update settings are unchanged.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		printResult := func(result selfupdate.Result) {
			output.PrintDataTo(cmd.OutOrStdout(), cliUpdateOutput{
				CLIUpdate: result, CLIVersion: version, PID: os.Getpid(), Home: config.HomeDir(),
			}, resolveFormat())
		}
		if os.Getenv(updateReexecEnv) == "1" {
			printResult(selfupdate.Result{Status: "restarted", Version: version})
			return nil
		}
		path, err := os.Executable()
		if err != nil {
			return err
		}
		key, _ := base64.StdEncoding.DecodeString(skills.VerifyPublicKeyBase64)
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		result := selfupdate.Check(ctx, selfupdate.Options{
			Home: config.HomeDir(), Executable: path, Version: version,
			CDN: cdnBase(), Key: key, CheckNow: true,
		})
		cancelled := ctx.Err()
		stop()
		if cancelled != nil {
			return cancelled
		}
		if result.Status == "updated" {
			return reexecCLIUpdate(cmd, result.Executable)
		}
		printResult(result)
		if result.Status != "current" {
			return fmt.Errorf("CLI update %s: %s", result.Status, result.Error)
		}
		return nil
	},
}

func reexecCLIUpdate(cmd *cobra.Command, path string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	child := exec.CommandContext(ctx, path, heartbeatReexecArgs(os.Args[1:], config.HomeDir(), serverFlag)...)
	child.Env = heartbeatReexecEnvironment(os.Environ())
	child.Stdin, child.Stdout, child.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := runHeartbeatReexec(child); err != nil {
		return &updatedCLIError{err}
	}
	return nil
}

func init() {
	rootCmd.AddCommand(updateCmd)
}
