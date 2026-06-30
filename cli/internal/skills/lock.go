package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Lock is a best-effort cross-process advisory lock implemented with an
// exclusive-create lock file. We avoid golang.org/x/sys flock so the behavior
// is identical on POSIX and Windows. A lock file older than staleLockSeconds is
// assumed to be crash debris and force-claimed.
type Lock struct {
	path string
	f    *os.File
	pid  int
}

// acquireLock tries to create the lock file under parent. Returns
// (lock, true, nil) on success, (nil, false, nil) when another live process
// holds it (caller should give up — for --quiet/--if-stale hooks that is
// harmless), or (nil, false, err) on a real filesystem error.
func acquireLock(parent string) (*Lock, bool, error) {
	path := filepath.Join(parent, lockFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			if isStaleLock(path) {
				os.Remove(path)
				// One retry after reaping stale debris.
				f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
				if err != nil {
					if os.IsExist(err) {
						return nil, false, nil
					}
					return nil, false, err
				}
			} else {
				return nil, false, nil
			}
		} else {
			return nil, false, err
		}
	}
	pid := os.Getpid()
	fmt.Fprintf(f, "%d %d\n", pid, time.Now().Unix())
	f.Sync()
	return &Lock{path: path, f: f, pid: pid}, true, nil
}

// Release closes and removes the lock file — but only if it still belongs to
// us. If a long pause made another process treat our lock as stale and reclaim
// it, the file now holds that process's pid; removing it blindly would delete
// THEIR live lock and let a third writer race their swap. So we re-read and
// only remove when the recorded pid matches ours.
func (l *Lock) Release() {
	if l == nil {
		return
	}
	l.f.Close()
	if data, err := os.ReadFile(l.path); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 1 {
			if pid, err := strconv.Atoi(fields[0]); err == nil && pid != l.pid {
				return // reclaimed by another process; do not delete their lock
			}
		}
	}
	os.Remove(l.path)
}

// isStaleLock reports whether the lock file's recorded timestamp is older than
// the staleness window. Falls back to file mtime if the contents are unreadable.
func isStaleLock(path string) bool {
	data, err := os.ReadFile(path)
	if err == nil {
		fields := strings.Fields(string(data))
		if len(fields) == 2 {
			if ts, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				return time.Now().Unix()-ts > staleLockSeconds
			}
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) > staleLockSeconds*time.Second
}
