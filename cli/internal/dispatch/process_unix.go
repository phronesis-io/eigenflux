//go:build !windows

package dispatch

import (
	"os/exec"
	"syscall"
)

type processGuard struct{ cmd *exec.Cmd }

func validateCommandLine(cmd *exec.Cmd) error { return nil }

func startManaged(cmd *exec.Cmd) (*processGuard, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &processGuard{cmd}, nil
}
func (g *processGuard) kill() {
	if g.cmd.Process != nil {
		_ = syscall.Kill(-g.cmd.Process.Pid, syscall.SIGKILL)
	}
}
func (g *processGuard) close() { g.kill() }
