package feedevent

import (
	"os"
	"path/filepath"
	"time"
)

// Keep the inode after unlock so competing processes always lock the same file.
type lock struct{ f *os.File }

func acquireLock(dir string) (*lock, bool) { return acquireNamedLock(dir, lockFileName) }
func acquireNamedLock(dir, name string) (*lock, bool) {
	return acquireNamedLockFor(dir, name, 250*time.Millisecond)
}
func acquireNamedLockFor(dir, name string, wait time.Duration) (*lock, bool) {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, false
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, filePerm)
	if err != nil {
		return nil, false
	}
	deadline := time.Now().Add(wait)
	for {
		acquired, err := tryFileLock(f)
		if err != nil {
			f.Close()
			return nil, false
		}
		if acquired {
			return &lock{f: f}, true
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, false
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func (l *lock) release() {
	if l != nil {
		_ = unlockFile(l.f)
		_ = l.f.Close()
	}
}
