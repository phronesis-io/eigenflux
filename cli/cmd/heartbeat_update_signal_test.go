package cmd

import (
	"bufio"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/skills"
	"errors"
	"fmt"
	"github.com/spf13/cobra"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestHeartbeatUpdateRequiresKnownMode(t *testing.T) {
	for _, mode := range []string{"", "plugin", "unknown", "skill"} {
		t.Run(mode, func(t *testing.T) {
			tempHome(t)
			t.Setenv(updateReexecEnv, "0")
			old := clientMeta
			oldVersion, oldKey := version, skills.VerifyPublicKeyBase64
			version, skills.VerifyPublicKeyBase64 = "0.0.47", ""
			clientMeta.Mode = mode
			t.Cleanup(func() { clientMeta = old; version, skills.VerifyPublicKeyBase64 = oldVersion, oldKey })
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			r, reexec, err := updateHeartbeatCLI(&cobra.Command{}, cfg, "")
			if err != nil || reexec {
				t.Fatalf("unexpected reexec: %v", err)
			}
			// The test executable has no release key. Both known modes can
			// reach the updater; unknown modes must stop before it is invoked.
			if mode != "skill" && mode != "plugin" && r.Status != "skipped" {
				t.Fatalf("mode %q: %+v", mode, r)
			}
			if (mode == "skill" || mode == "plugin") && r.Status != "unconfigured" {
				t.Fatalf("known mode did not reach updater: %+v", r)
			}
		})
	}
}

func TestHeartbeatReexecProcess(t *testing.T) {
	switch os.Getenv("EF_UPDATE_PROCESS_TEST") {
	case "child":
		if os.Getenv("EF_UPDATE_IGNORE_SIGNAL") != "1" {
			ch := make(chan os.Signal, 1)
			signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
			fmt.Println("ready")
			<-ch
			os.Exit(37)
		}
		signal.Ignore(os.Interrupt, syscall.SIGTERM)
		fmt.Println("ready")
		time.Sleep(time.Minute)
		os.Exit(99)
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestHeartbeatReexecProcess$")
		child.Env = append(os.Environ(), "EF_UPDATE_PROCESS_TEST=child")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		err := runHeartbeatReexec(child)
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 37 {
			os.Exit(37)
		}
		if exit != nil && child.ProcessState.Exited() == false && os.Getenv("EF_UPDATE_IGNORE_SIGNAL") == "1" {
			os.Exit(38)
		}
		os.Exit(98)
	}
}

func TestHeartbeatReexecSignals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot send POSIX process signals")
	}
	for _, tc := range []struct {
		name   string
		sig    os.Signal
		ignore bool
		code   int
	}{{"TERM", syscall.SIGTERM, false, 37}, {"INT", os.Interrupt, false, 37}, {"forced", syscall.SIGTERM, true, 38}} {
		t.Run(tc.name, func(t *testing.T) {
			parent := exec.Command(os.Args[0], "-test.run=^TestHeartbeatReexecProcess$")
			parent.Env = append(os.Environ(), "EF_UPDATE_PROCESS_TEST=parent")
			if tc.ignore {
				parent.Env = append(parent.Env, "EF_UPDATE_IGNORE_SIGNAL=1")
			}
			out, err := parent.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := parent.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = parent.Process.Kill() })
			ready := make(chan bool, 1)
			go func() { ready <- bufio.NewScanner(out).Scan() }()
			select {
			case ok := <-ready:
				if !ok {
					t.Fatal("child not ready")
				}
			case <-time.After(15 * time.Second):
				t.Fatal("child startup timed out")
			}
			if err := parent.Process.Signal(tc.sig); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- parent.Wait() }()
			select {
			case err := <-done:
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != tc.code {
					t.Fatalf("exit = %v, want %d", err, tc.code)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("parent did not reap child")
			}
		})
	}
}
