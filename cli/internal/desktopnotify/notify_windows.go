package desktopnotify

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

//go:embed windows.ps1
var windowsScript string

//go:embed macos/Icon.png
var windowsIconPNG []byte

func platformRequest(ctx context.Context, action string, message Message) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	xml, tag, err := windowsToast(message)
	if err != nil {
		return err
	}
	var iconPNG []byte
	if action == "enable" {
		iconPNG = windowsIconPNG
	}
	input, err := json.Marshal(struct {
		Action     string `json:"action"`
		Executable string `json:"executable"`
		XML        string `json:"xml"`
		Tag        string `json:"tag"`
		IconPNG    []byte `json:"icon_png,omitempty"`
	}{action, executable, xml, tag, iconPNG})
	if err != nil {
		return err
	}
	// Use the OS Windows PowerShell, not a PATH-supplied executable. All data goes
	// through stdin; the script is static and execution policy is never changed.
	root := os.Getenv("SystemRoot")
	if root == "" {
		return errors.New("Windows SystemRoot is unavailable")
	}
	powershell := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	command := exec.CommandContext(ctx, powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", windowsScript)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	command.Stdin = bytes.NewReader(input)
	output, err := command.Output()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	result := strings.TrimSpace(string(output))
	if result == "permission_denied" {
		return ErrPermission
	}
	if result == "setup_required" {
		return errors.New("desktop notifications need initial setup")
	}
	if err != nil {
		return fmt.Errorf("Windows notification helper failed (check notification settings and PowerShell policy): %w", err)
	}
	if result != "accepted" {
		return errors.New("invalid Windows notification helper response")
	}
	return nil
}
