package dispatch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var ErrMainOnlyExecutionAdapter = errors.New("main_only_bridge_execution_adapter: watch acp-bridge belongs in main_command; bind a native Hermes or full execution adapter in command")

func (b Binding) validateExecutionCommand() error {
	if len(b.Command) == 0 {
		return nil
	}
	name := filepath.Base(b.Command[0])
	knownCLI := strings.EqualFold(name, "eigenflux") || strings.EqualFold(name, "eigenflux.exe")
	if current, err := os.Executable(); err == nil && !knownCLI {
		knownCLI = sameCLIInstallation(b.Command[0], current)
	}
	if !knownCLI {
		return nil
	}
	argv := append(append([]string{}, b.Command[1:]...), b.Args...)
	commands := []string{}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			return nil
		}
		if strings.HasPrefix(arg, "-") {
			flag, _, inline := strings.Cut(arg, "=")
			if !strings.HasPrefix(flag, "--") {
				short := strings.TrimPrefix(flag, "-")
				for len(short) > 0 {
					switch short[0] {
					case 'v', 'h':
						short = short[1:]
					case 's', 'f':
						if len(short) == 1 && !inline {
							i++
						}
						short = ""
					default:
						return nil
					}
				}
				continue
			}
			switch flag {
			case "--homedir", "--server", "--format", "--origin", "--runtime-mode", "--runtime-model",
				"--host", "--owner-url", "--main-session-id", "--workdir", "--token-file":
				if !inline {
					i++
				}
			case "--no-interactive", "--verbose", "--help":
			default:
				return nil
			}
			continue
		}
		commands = append(commands, arg)
		if len(commands) == 2 {
			if commands[0] == "watch" && commands[1] == "acp-bridge" {
				return ErrMainOnlyExecutionAdapter
			}
			return nil
		}
	}
	return nil
}
