package profilestate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRuntimeUpdateSerializesAcrossProcesses(t *testing.T) {
	home := t.TempDir()
	start := func(action string) <-chan error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t.Cleanup(cancel)
		child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRuntimeLockProcessHelper$")
		child.Env = append(os.Environ(), "EIGENFLUX_RUNTIME_LOCK_ACTION="+action, "EIGENFLUX_RUNTIME_LOCK_HOME="+home)
		done := make(chan error, 1)
		go func() { done <- child.Run() }()
		return done
	}
	release := filepath.Join(home, "release")
	defer os.WriteFile(release, nil, 0o600)
	first := start("hold")
	waitForRuntimeFile(t, filepath.Join(home, "held"))
	second := start("update")
	waitForRuntimeFile(t, filepath.Join(home, "attempting"))
	select {
	case err := <-second:
		t.Fatalf("second process bypassed held lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, done := range []<-chan error{first, second} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("sidecar subprocess timed out")
		}
	}
	got, err := LoadRuntime(home, "prod")
	if err != nil || got.Revision != 2 {
		t.Fatalf("concurrent updates lost: state=%+v err=%v", got, err)
	}
	// Windows reports synthesized mode bits; its file access uses inherited ACLs.
	if mode := fileMode(t, runtimeFilePath(home, "prod")); runtime.GOOS != "windows" && mode != 0o600 {
		t.Fatalf("runtime file mode=%o", mode)
	}
	other, err := LoadRuntime(home, "other")
	if err != nil || other != (RuntimeState{}) {
		t.Fatalf("identity crossed server scope: %+v %v", other, err)
	}
}

func TestRuntimeLockProcessHelper(t *testing.T) {
	action, home := os.Getenv("EIGENFLUX_RUNTIME_LOCK_ACTION"), os.Getenv("EIGENFLUX_RUNTIME_LOCK_HOME")
	if action == "" {
		return
	}
	if action == "update" {
		if err := os.WriteFile(filepath.Join(home, "attempting"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := UpdateRuntime(home, "prod", func(state *RuntimeState) (bool, error) {
		if action == "hold" {
			if err := os.WriteFile(filepath.Join(home, "held"), nil, 0o600); err != nil {
				return false, err
			}
			waitForRuntimeFile(t, filepath.Join(home, "release"))
		}
		state.Revision++
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func waitForRuntimeFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("barrier file did not arrive: %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
