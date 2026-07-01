package feedevent

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// lock is a best-effort cross-process advisory lock (exclusive-create file, same
// pattern as internal/skills) guarding the queue file against concurrent
// record/flush. A lock older than staleLockSecond is reclaimed as crash debris.
type lock struct {
	path string
	f    *os.File
	pid  int
}

func acquireLock(dir string) (*lock, bool) {
	path := filepath.Join(dir, lockFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			if isStale(path) {
				os.Remove(path)
				f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
				if err != nil {
					return nil, false
				}
			} else {
				return nil, false
			}
		} else {
			return nil, false
		}
	}
	pid := os.Getpid()
	f.WriteString(strconv.Itoa(pid) + " " + strconv.FormatInt(time.Now().Unix(), 10) + "\n")
	f.Sync()
	return &lock{path: path, f: f, pid: pid}, true
}

func (l *lock) release() {
	if l == nil {
		return
	}
	l.f.Close()
	// Only remove if still ours (a reclaimed-as-stale steal must not be deleted).
	if data, err := os.ReadFile(l.path); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 1 {
			if pid, err := strconv.Atoi(fields[0]); err == nil && pid != l.pid {
				return
			}
		}
	}
	os.Remove(l.path)
}

func isStale(path string) bool {
	data, err := os.ReadFile(path)
	if err == nil {
		if fields := strings.Fields(string(data)); len(fields) == 2 {
			if ts, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				return time.Now().Unix()-ts > staleLockSecond
			}
		}
	}
	if info, err := os.Stat(path); err == nil {
		return time.Since(info.ModTime()) > staleLockSecond*time.Second
	}
	return false
}
