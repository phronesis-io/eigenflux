//go:build windows

package selfupdate

import (
	"crypto/ed25519"
	"errors"
	"os/exec"
)

func forwardInstallation(installation *Installation, key ed25519.PublicKey, args, env []string) (bool, int, error) {
	// Only reexecWatch can establish a restart-capable launcher. A generic
	// command may inherit watch identity variables without being a watch.
	err := runInstallationChildren(installation, key, args, env, false)
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		return true, exited.ExitCode(), nil
	}
	return true, 0, err
}
