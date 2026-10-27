package watchservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNotificationLaunchAgentPreservesExistingWrapper(t *testing.T) {
	for _, mode := range []string{"prepare", "start", "active", "disabled", "wrong_account", "ambiguous", "disabled_os", "writable", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, "Library", "LaunchAgents")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			cli := filepath.Join(home, "eigenflux")
			if err := os.WriteFile(cli, []byte("fixture"), 0700); err != nil {
				t.Fatal(err)
			}
			task := NotificationTask{Home: home, Server: "eigenflux", Dispatch: true, AgentID: "agent", PrincipalID: "owner", PrepareOnly: mode == "prepare" || mode == "active"}
			wrapper := filepath.Join(home, "run watch.sh")
			content := "#!/bin/bash\nset -eu\numask 077\nexport PATH=\"/usr/bin:/bin\"\nexec '" + cli + "' --homedir '" + home + "' --server eigenflux watch --dispatch\n"
			if mode == "wrong_account" {
				content = strings.Replace(content, "--server eigenflux", "--server other", 1)
			}
			if err := os.WriteFile(wrapper, []byte(content), 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "watch.plist")
			if err := os.WriteFile(file, []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "ambiguous" {
				if err := os.WriteFile(filepath.Join(dir, "other.plist"), []byte("unchanged"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "writable" {
				if err := os.Chmod(file, 0666); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "symlink" {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(wrapper, file); err != nil {
					t.Fatal(err)
				}
			}
			definition, _ := json.Marshal(notificationLaunchDefinition{Label: "ai.eigenflux.fixture", ProgramArguments: []string{"/bin/bash", wrapper}, WorkingDirectory: home, Disabled: mode == "disabled"})
			var mutations []string
			loaded := mode != "start"
			run := func(_ context.Context, program string, args ...string) ([]byte, error) {
				if program == "/usr/bin/plutil" {
					return definition, nil
				}
				if args[0] == "print-disabled" && mode == "disabled_os" {
					return []byte("\"ai.eigenflux.fixture\" => true\n"), nil
				}
				if args[0] == "print-disabled" || args[0] == "print" && strings.Count(args[1], "/") == 1 {
					return nil, nil
				}
				switch args[0] {
				case "print":
					if !loaded {
						return nil, errors.New("absent")
					}
					if mode == "active" || len(mutations) > 0 {
						return []byte("\tstate = running\n\tpid = 24680\n"), nil
					}
					return []byte("\tstate = waiting\n"), nil
				case "bootout", "bootstrap", "kickstart":
					mutations = append(mutations, args[0])
					loaded = args[0] != "bootout"
				default:
					t.Fatal(args)
				}
				return nil, nil
			}
			result, err := startNotificationLaunchAgent(context.Background(), task, home, os.Getuid(), run)
			if mode == "active" || mode == "disabled" || mode == "wrong_account" || mode == "ambiguous" || mode == "disabled_os" || mode == "writable" || mode == "symlink" {
				if err == nil || len(mutations) != 0 {
					t.Fatalf("unsafe result %+v %v mutations=%v", result, err, mutations)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "prepare" && result.Status != "task_stopped" || mode == "start" && result.PID != 24680 {
				t.Fatalf("result %+v", result)
			}
			raw, _ := os.ReadFile(file)
			if string(raw) != "unchanged" {
				t.Fatal("plist overwritten")
			}
			raw, _ = os.ReadFile(wrapper)
			if string(raw) != content {
				t.Fatal("wrapper overwritten")
			}
		})
	}
}

func TestNotificationWrapperRejectsUnverifiableCommands(t *testing.T) {
	for _, raw := range []string{"exec $CLI --homedir /x", "exec /bin/echo hi; touch /tmp/x", "source /tmp/config\nexec /bin/echo", "exec /bin/echo\nexit 0", "exec 'unterminated"} {
		if _, err := notificationWrapperCommand(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
