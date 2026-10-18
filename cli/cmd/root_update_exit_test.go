package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

const rootUpdateExitProcessEnv = "EF_ROOT_UPDATE_EXIT_PROCESS"

func TestRootUpdateExitProcess(t *testing.T) {
	mode := os.Getenv(rootUpdateExitProcessEnv)
	if mode == "" {
		return
	}
	code, err := strconv.Atoi(os.Getenv("EF_ROOT_UPDATE_EXIT_CODE"))
	if err != nil {
		t.Fatal(err)
	}
	if mode == "child" {
		if os.Getenv("EF_ROOT_UPDATE_EXIT_SIGNAL") == "1" {
			process, err := os.FindProcess(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			if err := process.Signal(syscall.Signal(code)); err != nil {
				t.Fatal(err)
			}
			time.Sleep(5 * time.Second)
			os.Exit(99)
		}
		os.Exit(code)
	}
	// Replace the command only in this subprocess, avoiding real Home/config
	// initialization while exercising the production Execute error branch.
	rootCmd = &cobra.Command{Use: "exit-test", RunE: func(*cobra.Command, []string) error {
		child := exec.Command(os.Args[0], "-test.run=^TestRootUpdateExitProcess$")
		child.Env = append(os.Environ(), rootUpdateExitProcessEnv+"=child")
		err := child.Run()
		if err == nil || mode == "ordinary" {
			return err
		}
		return fmt.Errorf("replacement CLI: %w", &updatedCLIError{err})
	}}
	rootCmd.SetArgs([]string{})
	Execute()
	os.Exit(0)
}

func TestExecuteUpdatedCLIExitStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   int
		signal bool
		mode   string
		want   int
	}{
		{"success", 0, false, "updated", 0},
		{"auth", 4, false, "updated", 4},
		{"business", 37, false, "updated", 37},
		{"ordinary error", 37, false, "ordinary", 2},
		{"SIGTERM", int(syscall.SIGTERM), true, "updated", 128 + int(syscall.SIGTERM)},
		{"SIGINT", int(syscall.SIGINT), true, "updated", 128 + int(syscall.SIGINT)},
		{"SIGKILL", int(syscall.SIGKILL), true, "updated", 128 + int(syscall.SIGKILL)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.signal && runtime.GOOS == "windows" {
				t.Skip("Windows does not support POSIX process signals")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			parent := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRootUpdateExitProcess$")
			signal := "0"
			if tc.signal {
				signal = "1"
			}
			parent.Env = append(os.Environ(), rootUpdateExitProcessEnv+"="+tc.mode,
				"EF_ROOT_UPDATE_EXIT_CODE="+strconv.Itoa(tc.code), "EF_ROOT_UPDATE_EXIT_SIGNAL="+signal)
			out, err := parent.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("process timed out: %s", out)
			}
			got := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatalf("run process: %v; %s", err, out)
				}
				got = exit.ExitCode()
			}
			if got != tc.want {
				t.Fatalf("exit = %d, want %d; output: %s", got, tc.want, out)
			}
		})
	}
}
