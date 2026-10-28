//go:build !windows

package selfupdate

import (
	"crypto/ed25519"

	"golang.org/x/sys/unix"
)

func forwardInstallation(installation *Installation, _ ed25519.PublicKey, args, env []string) (bool, int, error) {
	err := unix.Exec(installation.Executable, append([]string{installation.Executable}, args...), env)
	return true, 0, err
}
