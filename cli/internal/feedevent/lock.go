package feedevent

import (
	"cli.eigenflux.ai/internal/filelock"
	"os"
	"path/filepath"
	"time"
)

type lock struct{ held *filelock.Lock }

func acquireLock(dir string) (*lock, bool) { return acquireNamedLock(dir, lockFileName) }
func acquireNamedLock(dir, name string) (*lock, bool) {
	return acquireNamedLockFor(dir, name, 250*time.Millisecond)
}
func acquireNamedLockFor(dir, name string, wait time.Duration) (*lock, bool) {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, false
	}
	deadline := time.Now().Add(wait)
	for {
		held, ok, err := filelock.TryAcquire(filepath.Join(dir, name))
		if err != nil {
			return nil, false
		}
		if ok {
			return &lock{held: held}, true
		}
		if !time.Now().Before(deadline) {
			return nil, false
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func (l *lock) release() {
	if l != nil {
		l.held.Release()
	}
}
