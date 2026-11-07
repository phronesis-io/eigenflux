//go:build windows

package govern

import "os"

// ownerAlive reports whether a process with the given pid is alive on Windows.
// Unlike Unix, os.FindProcess fails (OpenProcess returns an error) when the pid
// does not exist, so a successful find is a live owner. The handle opened by
// FindProcess is released immediately to avoid leaking it on every check.
func ownerAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
