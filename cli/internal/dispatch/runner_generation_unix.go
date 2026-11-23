//go:build !windows

package dispatch

import (
	"errors"
	"math"
	"syscall"
)

// GenerationProcessGone probes the group startManaged created. A reused group
// ID only delays recovery. Never signal or terminate a historical PID.
func GenerationProcessGone(pid int) (bool, error) {
	if pid <= 1 || pid > math.MaxInt32 {
		return false, errors.New("invalid_generation_pid")
	}
	err := syscall.Kill(-pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	return false, err
}
