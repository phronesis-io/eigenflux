package govern

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LockFileName is the advisory process lock for a governance directory.
const LockFileName = "governance.lock"

// Advisory lock — mechanism and honest limitations.
//
// golang.org/x/sys is not a dependency of this module (and this slice adds no
// new dependencies), so a real kernel-enforced lock (syscall.Flock on Unix,
// LockFileEx on Windows) is not available. What is here is a stdlib-only,
// ADVISORY lock: a lockfile created with O_CREATE|O_EXCL whose mere existence
// claims the directory. It is deliberately NOT kernel-enforced:
//
//   - Two cooperating Hub processes honour it, so they cannot interleave audit
//     writes. Any other program that ignores the file can still write to the
//     directory; this is cooperation, not access control.
//   - There is no file descriptor held open as the lock (the lockfile is
//     created, written, and closed), so a killed process cannot rely on the OS
//     to release it for us. Recovery is via stale detection below: a lockfile
//     whose recorded owner pid is no longer alive is removed and reclaimed.
//     PID reuse (a dead pid reassigned to a new live process) errs safe: the
//     lock is respected, never stolen. A zombie/unreaped owner likewise reads
//     as alive and is respected rather than evicted.
//   - Reclaiming a stale lock has a small time-of-check-to-time-of-use window:
//     two processes could both observe the same stale file and both create an
//     O_EXCL successor. This is inherent to lockfile schemes without a kernel
//     primitive and is the price of the zero-dependency design.
//   - Staleness is based on process liveness, NOT a time lease. A plain TTL
//     would wrongly evict a long-running live Hub whose lockfile is old.
//     The timestamp in the file is therefore for operator diagnosis only.
//   - The lock is exclusive between processes but reentrant within one process:
//     several Store handles on one directory share a reference-counted claim so
//     single-process callers (and tests) never deadlock against themselves.
//     That in-process multiplicity was already allowed before the lock existed.
//
// A kernel-enforced lock can replace this by swapping createLockFile/ownerAlive
// for platform calls once x/sys is an accepted dependency; the Store-side
// acquire/release/Close contract would not change.

// lockFile is the self-describing content of the lockfile. PID is the primary
// stale signal (a lock left by a dead owner is reclaimable); AcquiredAt is a
// timestamp for operator diagnosis, so a leftover file reads as "held by pid
// 1234 since <time>" rather than an opaque blob.
type lockFile struct {
	PID        int       `json:"pid"`
	AcquiredAt time.Time `json:"acquired_at"`
	Host       string    `json:"host,omitempty"`
}

// ownerIsAlive is the process-liveness check used by stale detection. It is a
// variable so tests can force the two outcomes deterministically without
// depending on a real second process; production always uses ownerAlive.
var ownerIsAlive = ownerAlive

// processLocks tracks, per process, the directories this process already holds.
//
// The OS-level lock is exclusive between *processes*, but several Store handles
// in one process (tests, in-process tooling, a restart within one binary) must
// not deadlock against each other. A reference count makes the lock reentrant
// within the process while a second process still sees the lockfile and is
// refused. Cross-process exclusion is the goal; in-process multiplicity was
// already allowed before the lock existed.
var processLocks = struct {
	sync.Mutex
	byDir map[string]*processLock
}{byDir: make(map[string]*processLock)}

type processLock struct {
	path string
	refs int
}

// processLockHandle is one Store's claim on a shared processLock.
type processLockHandle struct {
	key string
}

// acquireProcessLock claims dir for this process, or returns an error when a
// different live process already holds it.
func acquireProcessLock(dir string) (*processLockHandle, error) {
	key, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve governance dir %s: %w", dir, err)
	}
	key = filepath.Clean(key)
	path := filepath.Join(key, LockFileName)

	processLocks.Lock()
	defer processLocks.Unlock()
	if existing, ok := processLocks.byDir[key]; ok {
		existing.refs++
		return &processLockHandle{key: key}, nil
	}
	if err := createLockFile(path); err != nil {
		return nil, err
	}
	processLocks.byDir[key] = &processLock{path: path, refs: 1}
	return &processLockHandle{key: key}, nil
}

// release drops one reference, removing the lockfile when the last Store in
// this process lets go. It is idempotent: a second release is a no-op.
func (h *processLockHandle) release() error {
	if h == nil {
		return nil
	}
	processLocks.Lock()
	defer processLocks.Unlock()
	pl, ok := processLocks.byDir[h.key]
	if !ok {
		return nil
	}
	pl.refs--
	if pl.refs > 0 {
		return nil
	}
	delete(processLocks.byDir, h.key)
	if err := os.Remove(pl.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("release governance lock %s: %w", pl.path, err)
	}
	return nil
}

// createLockFile atomically claims path. On an existing lockfile it inspects the
// incumbent: a dead (or obviously invalid) owner makes the lock stale and it is
// removed and reclaimed; a live foreign owner is refused.
func createLockFile(path string) error {
	content, err := encodeLockFile(lockFile{PID: os.Getpid(), AcquiredAt: time.Now(), Host: hostName()})
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if _, werr := f.Write(content); werr != nil {
				_ = f.Close()
				_ = os.Remove(path)
				return fmt.Errorf("write governance lock %s: %w", path, werr)
			}
			if cerr := f.Close(); cerr != nil {
				_ = os.Remove(path)
				return fmt.Errorf("close governance lock %s: %w", path, cerr)
			}
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create governance lock %s: %w", path, err)
		}
		incumbent, rerr := readLockFile(path)
		if rerr != nil {
			return fmt.Errorf("governance lock %s is held by an unreadable owner: %w", path, rerr)
		}
		if !lockIsStale(incumbent) {
			return fmt.Errorf("governance directory %s is already locked by pid %d since %s",
				filepath.Dir(path), incumbent.PID, incumbent.AcquiredAt.Format(time.RFC3339))
		}
		// Stale: reclaim. There is a narrow TOCTOU window here (another process
		// could reclaim between our remove and our O_EXCL); that is an inherent
		// limitation of an advisory lockfile and is documented as such.
		if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return fmt.Errorf("remove stale governance lock %s: %w", path, rerr)
		}
	}
	return fmt.Errorf("could not acquire governance lock %s", path)
}

// lockIsStale reports whether a lockfile may be reclaimed. A missing/zero pid
// or a pid whose process is gone means the owner died without releasing; a
// live process is authoritative regardless of how old the timestamp is — this
// is why a plain time lease is NOT used, because it would wrongly evict a
// long-running live Hub. PID reuse (a dead pid reassigned to a new live
// process) errs on the safe side: the lock is respected, never stolen.
func lockIsStale(lf lockFile) bool {
	if lf.PID <= 0 {
		return true
	}
	return !ownerIsAlive(lf.PID)
}

func encodeLockFile(lf lockFile) ([]byte, error) {
	b, err := json.MarshalIndent(lf, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode governance lock: %w", err)
	}
	return append(b, '\n'), nil
}

func readLockFile(path string) (lockFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return lockFile{}, err
	}
	var lf lockFile
	if err := json.Unmarshal(b, &lf); err != nil {
		return lockFile{}, fmt.Errorf("decode governance lock: %w", err)
	}
	return lf, nil
}

func hostName() string {
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return ""
}
