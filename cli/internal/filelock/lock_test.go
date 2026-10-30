package filelock

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRetainedLockExclusiveAndLegacyMarker(t *testing.T) {
	for _, body := range []string{"", "123 1\n"} {
		t.Run(body, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".lock")
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Stat(path)
			l, ok, err := TryAcquire(path)
			if err != nil || !ok {
				t.Fatalf("abandoned marker blocked: %v %v", ok, err)
			}
			defer l.Release()
			old := time.Now().Add(-24 * time.Hour)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
			other, ok, err := TryAcquire(path)
			if other != nil {
				other.Release()
			}
			if err != nil || ok {
				t.Fatalf("live lock stolen: %v %v", ok, err)
			}
			if idle, err := Idle(path); err != nil || idle {
				t.Fatalf("held lock appears idle: %v %v", idle, err)
			}
			l.Release()
			l.Release()
			after, err := os.Stat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("lock file removed or replaced", err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != body {
				t.Fatal("marker contents modified", err)
			}
			if idle, err := Idle(path); err != nil || !idle {
				t.Fatalf("released lock busy: %v %v", idle, err)
			}
			next, ok, err := TryAcquire(path)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			next.Release()
		})
	}
}

func TestIdleDoesNotCreateOrWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	if idle, err := Idle(path); err != nil || !idle {
		t.Fatal(idle, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("read probe created file", err)
	}
	if err := os.WriteFile(path, nil, 0400); err != nil {
		t.Fatal(err)
	}
	if idle, err := Idle(path); err != nil || !idle {
		t.Fatal(idle, err)
	}
	if _, _, err := TryAcquire(filepath.Join(t.TempDir(), "missing", ".lock")); !os.IsNotExist(err) {
		t.Fatalf("error hidden: %v", err)
	}
}

func TestLockHelperProcess(t *testing.T) {
	path := os.Getenv("EF_FILELOCK_TEST_PATH")
	if path == "" {
		return
	}
	l, ok, err := TryAcquire(path)
	if err != nil || !ok {
		os.Exit(2)
	}
	defer l.Release()
	_, _ = os.Stdout.WriteString("locked\n")
	time.Sleep(time.Hour)
}

func TestProcessCrashReleasesLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelperProcess$")
	cmd.Env = append(os.Environ(), "EF_FILELOCK_TEST_PATH="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	ready := make(chan string, 1)
	go func() { s, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- s }()
	select {
	case s := <-ready:
		if s != "locked\n" {
			t.Fatalf("child failed: %q", s)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child startup timed out")
	}
	l, ok, err := TryAcquire(path)
	if l != nil {
		l.Release()
	}
	if err != nil || ok {
		t.Fatalf("cross-process lock failed: %v %v", ok, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	l, ok, err = TryAcquire(path)
	if err != nil || !ok {
		t.Fatalf("crash left lock busy: %v %v", ok, err)
	}
	l.Release()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("crash recovery deleted lock", err)
	}
}
