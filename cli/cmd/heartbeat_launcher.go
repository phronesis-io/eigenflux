package cmd

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/heartbeatmigration"
)

func nativeHeartbeatLauncher(home, server, mode, shellName string) (string, error) {
	bin, err := stableInstalledExecutable()
	if err != nil {
		return "", err
	}
	return renderHeartbeatLauncher(bin, home, server, mode, shellName)
}

func nativeHeartbeatCLIPrefix(home, server, shellName string, mode ...string) (string, error) {
	bin, err := stableInstalledExecutable()
	if err != nil {
		return "", err
	}
	return renderHeartbeatCLIPrefix(bin, home, server, shellName, mode...)
}

func heartbeatShellName(shellName string) string {
	if shellName == "" || shellName == "auto" {
		shellName = "posix"
		if runtime.GOOS == "windows" {
			shellName = "powershell"
		}
	}
	return shellName
}

// Keep policy checks and rendered execution commands on the same argument list.
func heartbeatCLIPrefixArgs(bin, home, server string, mode ...string) ([]string, error) {
	if !filepath.IsAbs(home) || server == "" {
		return nil, fmt.Errorf("absolute Home and explicit server required")
	}
	args := []string{bin, "--homedir", home, "--server", server}
	if len(mode) > 0 && mode[0] != "" {
		args = append(args, "--runtime-mode", mode[0])
	}
	return args, nil
}

func heartbeatLauncherArgs(bin, home, server, mode, shellName string) ([]string, error) {
	shellName = heartbeatShellName(shellName)
	if shellName != "posix" && shellName != "powershell" && shellName != "cmd" {
		return nil, fmt.Errorf("unsupported scheduler shell %q", shellName)
	}
	args, err := heartbeatCLIPrefixArgs(bin, home, server)
	if err != nil {
		return nil, err
	}
	args = append(args, "--runtime-mode", mode, "heartbeat", "plan", "--shell", shellName, "--format", "agent")
	if shellName == "cmd" {
		for _, arg := range args {
			if strings.ContainsAny(arg, "\r\n\"%!^&|<>") {
				return nil, fmt.Errorf("launcher value cannot be represented safely in cmd; use PowerShell")
			}
		}
	}
	return args, nil
}

func renderHeartbeatCLIPrefix(bin, home, server, shellName string, mode ...string) (string, error) {
	shellName = heartbeatShellName(shellName)
	args, err := heartbeatCLIPrefixArgs(bin, home, server, mode...)
	if err != nil {
		return "", err
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
	shellName = heartbeatShellName(shellName)
	args, err := heartbeatLauncherArgs(bin, home, server, mode, shellName)
	if err != nil {
		return "", err
	}
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
