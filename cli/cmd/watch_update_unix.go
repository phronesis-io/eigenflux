//go:build !windows

package cmd

import "golang.org/x/sys/unix"

// The replacement inherits the same PID, working directory and standard I/O.
func reexecWatch(path string, args, env []string) error {
	return unix.Exec(path, append([]string{path}, args...), env)
}
