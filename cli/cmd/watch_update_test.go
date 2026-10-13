package cmd

import (
	"cli.eigenflux.ai/internal/auth"
	watchstate "cli.eigenflux.ai/internal/watch"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestWatchHandoffDrainsAdmittedPulls(t *testing.T) {
	h := newWatchHandoff()
	for range 3 {
		if !h.begin() {
			t.Fatal("initial admission denied")
		}
	}
	h.request("1.0.1")
	if h.begin() {
		t.Fatal("admitted a new task during handoff")
	}
	for range 2 {
		h.end()
		select {
		case <-h.done:
			t.Fatal("handoff before every existing task completed")
		default:
		}
	}
	h.end()
	select {
	case <-h.done:
	default:
		t.Fatal("idle handoff not released")
	}
	h.request("1.0.2")
	if h.version() != "1.0.1" {
		t.Fatal("handoff target mutated")
	}
	idle := newWatchHandoff()
	idle.request("1.0.1")
	select {
	case <-idle.done:
	default:
		t.Fatal("idle handoff blocked")
	}
}

func TestWatchReexecPinsIdentityAndPreservesEnvironment(t *testing.T) {
	identity := auth.V2Credentials{AgentID: "agent-1", PrincipalID: "principal-1"}
	env := watchReexecEnvironment([]string{"PATH=/usr/bin", "CUSTOM=secret value", "EIGENFLUX_HOME=old", watchAgentEnv + "=old", watchPrincipalEnv + "=old", updateReexecEnv + "=1", updateAttemptEnv + "=old"}, "/stable home", identity)
	want := []string{"PATH=/usr/bin", "CUSTOM=secret value", "EIGENFLUX_HOME=/stable home", watchAgentEnv + "=agent-1", watchPrincipalEnv + "=principal-1"}
	if !reflect.DeepEqual(env, want) {
		t.Fatal("lost inherited environment or retained stale identity")
	}
	args := []string{"--server", "old", "watch", "--runtime-mode", "plugin", "--runtime-model", "test model", "--homedir", "old", "--"}
	got := heartbeatReexecArgs(args, "/stable home", "pinned server")
	if !reflect.DeepEqual(got, append(args[:len(args)-1], "--homedir", "/stable home", "--server", "pinned server")) {
		t.Fatalf("arguments changed: %q", got)
	}
	t.Setenv(watchAgentEnv, identity.AgentID)
	t.Setenv(watchPrincipalEnv, identity.PrincipalID)
	if err := watchExpectedIdentity(&identity); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(watchExpectedIdentity(&auth.V2Credentials{AgentID: "different", PrincipalID: "principal-1"}), errWatchIdentity) {
		t.Fatal("replacement adopted another account")
	}
}

func TestWatchReexecProcessReleasesAccountLock(t *testing.T) {
	if os.Getenv("EF_WATCH_REEXEC_FIXTURE") != "" {
		return
	}
	dir := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestWatchReexecProcessHelper$")
	child.Env = append(os.Environ(), "EF_WATCH_REEXEC_FIXTURE=first", "EF_WATCH_REEXEC_HOME="+dir)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("handoff failed: %v %s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "adopted.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		PID, OriginalPID int
		Env              string
		Args             []string
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Env != "agent" || len(result.Args) != 2 || result.Args[1] != "literal value" {
		t.Fatalf("lost identity or args: %+v", result)
	}
	if runtime.GOOS != "windows" && result.PID != result.OriginalPID {
		t.Fatal("POSIX handoff changed PID")
	}
}

func TestWatchReexecProcessHelper(t *testing.T) {
	stage := os.Getenv("EF_WATCH_REEXEC_FIXTURE")
	if stage == "" {
		return
	}
	home := os.Getenv("EF_WATCH_REEXEC_HOME")
	release, err := watchstate.Acquire(home, "pinned")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if stage == "first" {
		env := watchReexecEnvironment(os.Environ(), home, auth.V2Credentials{AgentID: "agent", PrincipalID: "principal"})
		for i, v := range env {
			if strings.HasPrefix(v, "EF_WATCH_REEXEC_FIXTURE=") {
				env[i] = "EF_WATCH_REEXEC_FIXTURE=second"
			}
		}
		env = append(env, "EF_WATCH_REEXEC_PID="+strconv.Itoa(os.Getpid()))
		release()
		if err := reexecWatch(os.Args[0], []string{"-test.run=^TestWatchReexecProcessHelper$", "--", "literal value"}, env); err != nil {
			t.Fatal(err)
		}
		return
	}
	original, _ := strconv.Atoi(os.Getenv("EF_WATCH_REEXEC_PID"))
	result := struct {
		PID, OriginalPID int
		Env              string
		Args             []string
	}{os.Getpid(), original, os.Getenv(watchAgentEnv), os.Args[len(os.Args)-2:]}
	raw, _ := json.Marshal(result)
	if err := os.WriteFile(filepath.Join(home, "adopted.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}
