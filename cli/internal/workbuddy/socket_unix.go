//go:build darwin || linux

package workbuddy

import (
	"errors"
	"os"
	"syscall"
)

func socketOwner(path string) error {
	st, e := os.Lstat(path)
	if e != nil || st.Mode()&os.ModeSocket == 0 || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid socket")
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(sys.Uid) != os.Getuid() || st.Mode().Perm()&0022 != 0 {
		return errors.New("untrusted socket")
	}
	return nil
}
