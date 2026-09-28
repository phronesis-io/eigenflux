package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/heartbeatmigration"
)

func nativeHeartbeatLauncher(home, server, mode, shellName string) (string, error) {
	bin, err := os.Executable()
	if err != nil {
		return "", err
	}
	return renderHeartbeatLauncher(bin, home, server, mode, shellName)
}

func nativeHeartbeatCLIPrefix(home, server, shellName string, mode ...string) (string, error) {
	bin, err := os.Executable()
	if err != nil {
		return "", err
	}
	return renderHeartbeatCLIPrefix(bin, home, server, shellName, mode...)
}

func renderHeartbeatCLIPrefix(bin, home, server, shellName string, mode ...string) (string, error) {
	if shellName == "" || shellName == "auto" {
		shellName = "posix"
		if runtime.GOOS == "windows" {
			shellName = "powershell"
		}
	}
	if !filepath.IsAbs(home) || server == "" {
		return "", fmt.Errorf("absolute Home and explicit server required")
	}
	args := []string{bin, "--homedir", home, "--server", server}
	if len(mode) > 0 && mode[0] != "" {
		args = append(args, "--runtime-mode", mode[0])
	}
	switch shellName {
	case "posix":
		for i := range args {
			args[i] = shellQuote(args[i])
		}
		return strings.Join(args, " "), nil
	case "powershell":
		for i := range args {
			args[i] = "'" + strings.ReplaceAll(args[i], "'", "''") + "'"
		}
		return "& " + strings.Join(args, " "), nil
	case "cmd":
		for i, arg := range args {
			if strings.ContainsAny(arg, "\r\n\"%!^&|<>") {
				return "", fmt.Errorf("CLI prefix cannot be represented safely in cmd; use PowerShell")
			}
			args[i] = "\"" + arg + "\""
		}
		return strings.Join(args, " "), nil
	default:
		return "", fmt.Errorf("unsupported command shell %q", shellName)
	}
}

func renderHeartbeatLauncher(bin, home, server, mode, shellName string) (string, error) {
	if shellName == "" || shellName == "auto" {
		shellName = "posix"
		if runtime.GOOS == "windows" {
			shellName = "powershell"
		}
	}
	if server == "" || !filepath.IsAbs(home) {
		return "", fmt.Errorf("absolute Home and explicit server required")
	}
	args := []string{bin, "--homedir", home, "--server", server, "--runtime-mode", mode, "heartbeat", "plan", "--shell", shellName, "--format", "agent"}
	switch shellName {
	case "posix":
		for i := range args {
			args[i] = shellQuote(args[i])
		}
		return strings.Join(args, " "), nil
	case "powershell":
		quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
		for i := range args {
			args[i] = quote(args[i])
		}
		return "& " + strings.Join(args, " "), nil
	case "cmd":
		for _, arg := range append(append([]string{}, args...), mode) {
			if strings.ContainsAny(arg, "\r\n\"%!^&|<>") {
				return "", fmt.Errorf("launcher value cannot be represented safely in cmd; use PowerShell")
			}
		}
		for i := range args {
			args[i] = "\"" + args[i] + "\""
		}
		return strings.Join(args, " "), nil
	default:
		return "", fmt.Errorf("unsupported scheduler shell %q", shellName)
	}
}

// Legacy launchers may omit --server. Resolve only a uniquely configured and
// authenticated binding in their exact Home, never the currently selected default.
func resolveLegacyHeartbeatServers(in *heartbeatmigration.Inventory, home string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	for i := range in.Tasks {
		t := &in.Tasks[i]
		if t.Owner != "eigenflux" || t.Purpose != "heartbeat" || t.Server != "" || filepath.Clean(t.Home) != filepath.Clean(home) {
			continue
		}
		if len(cfg.Servers) != 1 {
			return fmt.Errorf("task %s has no server; multiple configured bindings require an explicit choice", t.ID)
		}
		cred, err := auth.LoadV2Credentials(cfg.Servers[0].Name)
		if err != nil || cred.AgentID == "" {
			return fmt.Errorf("task %s has no verified single-server identity", t.ID)
		}
		t.Server = cfg.Servers[0].Name
	}
	return nil
}
