package profilestate

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

func TestStateRoundTripAndScopeIsolation(t *testing.T) {
	home := t.TempDir()
	want := State{LastRefreshUnix: 11, LastCheckedUnix: 22, LastPromptedUnix: 33}
	if err := Save(home, "prod", "101", want); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := Load(home, "prod", "101"); got != want {
		t.Fatalf("load = %+v, want %+v", got, want)
	}
	if got := Load(home, "prod", "202"); got != (State{}) {
		t.Fatalf("another account inherited state: %+v", got)
	}
	if got := Load(home, "staging", "101"); got != (State{}) {
		t.Fatalf("another server inherited state: %+v", got)
	}
	if mode := fileMode(t, FilePath(home, "prod", "101")); mode != 0o600 {
		t.Fatalf("state mode = %o, want 600", mode)
	}
}

func TestLockContentionTimesOutAndRecovers(t *testing.T) {
	path := lockPath(t.TempDir(), "prod", "101")
	first, err := acquireLock(path)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	start := time.Now()
	if _, err := acquireLock(path); !errors.Is(err, errLockTimeout) {
		t.Fatalf("contended lock error = %v, want %v", err, errLockTimeout)
	}
	if elapsed := time.Since(start); elapsed < lockWaitTimeout {
		t.Fatalf("lock timed out too early: %v", elapsed)
	}
	first.release()
	second, err := acquireLock(path)
	if err != nil {
		t.Fatalf("lock did not recover after release: %v", err)
	}
	second.release()
}

func TestKernelReleasesLockWhenProcessExits(t *testing.T) {
	if os.Getenv("EIGENFLUX_PROFILESTATE_LOCK_HELPER") == "1" {
		lock, err := acquireLock(os.Getenv("EIGENFLUX_PROFILESTATE_LOCK_PATH"))
		if err != nil || lock == nil {
			os.Exit(2)
		}
		os.Exit(0) // deliberately bypass release
	}

	path := lockPath(t.TempDir(), "prod", "101")
	cmd := exec.Command(os.Args[0], "-test.run=TestKernelReleasesLockWhenProcessExits")
	cmd.Env = append(os.Environ(),
		"EIGENFLUX_PROFILESTATE_LOCK_HELPER=1",
		"EIGENFLUX_PROFILESTATE_LOCK_PATH="+path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("lock helper: %v: %s", err, out)
	}
	lock, err := acquireLock(path)
	if err != nil {
		t.Fatalf("kernel did not release exited process lock: %v", err)
	}
	lock.release()
}

func TestUpdateSerializesConcurrentMutations(t *testing.T) {
	home := t.TempDir()
	const workers = 24
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Update(home, "prod", "101", func(state *State) bool {
				state.LastPromptedUnix++
				return true
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent update: %v", err)
		}
	}
	if got := Load(home, "prod", "101").LastPromptedUnix; got != workers {
		t.Fatalf("last_prompted_unix = %d, want %d", got, workers)
	}
}

func TestCorruptStateFailsOpenToZero(t *testing.T) {
	home := t.TempDir()
	path := FilePath(home, "prod", "101")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Load(home, "prod", "101"); got != (State{}) {
		t.Fatalf("corrupt state = %+v, want zero", got)
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
