package govern

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// lockTestStore opens a durable store and registers cleanup so the append
// handle and the advisory lock are released before the temp dir is removed
// (Windows refuses to delete a directory that still has an open file).
func lockTestStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func writeLockFileForTest(t *testing.T, path string, lf lockFile) {
	t.Helper()
	b, err := encodeLockFile(lf)
	if err != nil {
		t.Fatalf("encode lock: %v", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write lock: %v", err)
	}
}

// forceOwnerIsAlive swaps the process-liveness check so a test can force the
// "live" or "dead" outcome deterministically. Tests using it are not parallel
// and restore the original via defer.
func forceOwnerIsAlive(fn func(int) bool) func() {
	old := ownerIsAlive
	ownerIsAlive = fn
	return func() { ownerIsAlive = old }
}

func TestProcessLockAcquireAndRelease(t *testing.T) {
	dir := t.TempDir()
	s := lockTestStore(t, dir)
	lockPath := filepath.Join(dir, LockFileName)

	lf, err := readLockFile(lockPath)
	if err != nil {
		t.Fatalf("lock file not created/readable: %v", err)
	}
	if lf.PID != os.Getpid() {
		t.Fatalf("lock pid = %d, want this process %d", lf.PID, os.Getpid())
	}
	if lf.AcquiredAt.IsZero() {
		t.Fatal("lock file has no timestamp")
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file was not removed on Close: stat err = %v", err)
	}
}

func TestProcessLockReentrantInOneProcess(t *testing.T) {
	dir := t.TempDir()
	s1 := lockTestStore(t, dir)
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("a second Store in the same process must not deadlock: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	// Both handles remain usable: they share one advisory lock via a refcount.
	if err := s1.AppendAudit(AuditEntry{Seq: 1, CompanionID: "c", Outcome: OutcomeAllowed}); err != nil {
		t.Fatalf("append via first store: %v", err)
	}
	if err := s2.AppendAudit(AuditEntry{Seq: 2, CompanionID: "c", Outcome: OutcomeAllowed}); err != nil {
		t.Fatalf("append via second store: %v", err)
	}

	lockPath := filepath.Join(dir, LockFileName)
	if err := s2.Close(); err != nil {
		t.Fatalf("close second store: %v", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock released while another store in this process still holds it: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock not released after the last store closed: stat err = %v", err)
	}
}

func TestProcessLockStaleRecovery(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, LockFileName)
	// A lock left behind by a crashed Hub: an invalid pid and an old timestamp.
	writeLockFileForTest(t, lockPath, lockFile{
		PID: 0, AcquiredAt: time.Now().Add(-2 * time.Hour), Host: "crashed-hub",
	})

	s := lockTestStore(t, dir) // must detect stale and reclaim
	lf, err := readLockFile(lockPath)
	if err != nil {
		t.Fatalf("reclaimed lock is not readable: %v", err)
	}
	if lf.PID != os.Getpid() {
		t.Fatalf("stale lock not reclaimed by this process: pid = %d", lf.PID)
	}
	_ = s.Close()
}

func TestProcessLockStaleViaDeadOwner(t *testing.T) {
	restore := forceOwnerIsAlive(func(int) bool { return false })
	defer restore()

	dir := t.TempDir()
	lockPath := filepath.Join(dir, LockFileName)
	writeLockFileForTest(t, lockPath, lockFile{
		PID: 987654, AcquiredAt: time.Now().Add(-time.Hour), Host: "dead-hub",
	})

	s := lockTestStore(t, dir)
	lf, err := readLockFile(lockPath)
	if err != nil || lf.PID != os.Getpid() {
		t.Fatalf("lock with a dead owner was not reclaimed: lf=%+v err=%v", lf, err)
	}
	_ = s.Close()
}

func TestProcessLockRespectsLiveOwner(t *testing.T) {
	restore := forceOwnerIsAlive(func(int) bool { return true })
	defer restore()

	dir := t.TempDir()
	lockPath := filepath.Join(dir, LockFileName)
	writeLockFileForTest(t, lockPath, lockFile{
		PID: 424242, AcquiredAt: time.Now(), Host: "other-hub",
	})

	if _, err := OpenStore(dir); err == nil {
		t.Fatal("a lock held by a live foreign process must be refused")
	} else if !strings.Contains(err.Error(), "locked") {
		t.Fatalf("error does not name the lock: %v", err)
	}
	lf, err := readLockFile(lockPath)
	if err != nil || lf.PID != 424242 {
		t.Fatalf("the live owner's lockfile was disturbed: lf=%+v err=%v", lf, err)
	}
}

// TestProcessLockExcludesOtherProcess is the end-to-end proof: a genuinely
// separate process (whose pid is live) must be refused while this process
// holds the governance directory. The child re-runs this one test with a
// helper env var and reports the outcome through a result file.
func TestProcessLockExcludesOtherProcess(t *testing.T) {
	if os.Getenv("GOVERN_LOCK_HELPER") == "1" {
		runCrossProcessLockHelper()
		return
	}
	dir := t.TempDir()
	s := lockTestStore(t, dir) // this process holds the lock for the whole test

	resultPath := filepath.Join(t.TempDir(), "helper-result.txt")
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate test binary for cross-process check: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^TestProcessLockExcludesOtherProcess$")
	cmd.Env = append(os.Environ(),
		"GOVERN_LOCK_HELPER=1",
		"GOVERN_LOCK_DIR="+dir,
		"GOVERN_LOCK_RESULT="+resultPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper process failed: %v\n%s", err, out)
	}
	result, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("helper wrote no result: %v", err)
	}
	if !strings.HasPrefix(string(result), "refused") {
		t.Fatalf("a second process acquired a directory locked by the first: %q", result)
	}
	_ = s.Close()
}

func runCrossProcessLockHelper() {
	dir := os.Getenv("GOVERN_LOCK_DIR")
	resultPath := os.Getenv("GOVERN_LOCK_RESULT")
	result := "refused"
	st, err := OpenStore(dir)
	if err == nil {
		result = "acquired"
		_ = st.Close()
	} else {
		result = "refused: " + err.Error()
	}
	_ = os.WriteFile(resultPath, []byte(result), 0o600)
}
