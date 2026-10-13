//go:build windows

package cmd

import (
	"os"
	"os/exec"
)

func reexecWatch(path string, args, env []string) error {
	child := exec.Command(path, args...)
	child.Env = env
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	return runHeartbeatReexec(child)
}
