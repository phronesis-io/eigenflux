//go:build !windows

package selfupdate

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func tryLock(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}
func unlock(f *os.File)             { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }
func replace(from, to string) error { return os.Rename(from, to) }

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
