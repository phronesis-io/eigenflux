//go:build windows

package cmd

import (
	"os/exec"
	"syscall"
)

func detachPluginWorker(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000200 | 0x00000008, HideWindow: true}
}
