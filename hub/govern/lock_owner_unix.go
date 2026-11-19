//go:build !windows

package govern

import (
	"errors"
	"os"
	"syscall"
)

// ownerAlive reports whether a process with the given pid is alive on Unix.
// os.FindProcess succeeds even for a pid that does not exist, so a zero signal
// is used to distinguish a live owner from a dead one (this is the check the
// os package documents). EPERM means the process exists but belongs to another
// user — it is still alive and still holding the lock.
func ownerAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
