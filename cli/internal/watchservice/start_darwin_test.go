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
	for _, mode := range []string{"prepare", "start", "active", "disabled", "wrong_account", "ambiguous", "disabled_os", "writable", "symlink", "old_cli", "unverifiable_wrapper", "wrong_pin"} {
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
			task := NotificationTask{CLI: cli, Home: home, Server: "eigenflux", Dispatch: true, AgentID: "agent", PrincipalID: "owner", PrepareOnly: mode == "prepare" || mode == "active"}
			if mode == "old_cli" {
				task.CLI = filepath.Join(home, "new", "eigenflux")
				if err := os.MkdirAll(filepath.Dir(task.CLI), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(task.CLI, []byte("new"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			wrapper := filepath.Join(home, "run watch.sh")
			content := "#!/bin/bash\nset -eu\numask 077\nexport PATH=\"/usr/bin:/bin\"\nexec '" + cli + "' --homedir '" + home + "' --server eigenflux watch --dispatch\n"
			if mode == "unverifiable_wrapper" {
				content = "source /tmp/unknown\n" + content
			}
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
			def := notificationLaunchDefinition{Label: "ai.eigenflux.fixture", ProgramArguments: []string{"/bin/bash", wrapper}, WorkingDirectory: home, Disabled: mode == "disabled"}
			if mode == "wrong_pin" {
				def.EnvironmentVariables = map[string]string{"EIGENFLUX_WATCH_EXPECTED_AGENT": "other", "EIGENFLUX_WATCH_EXPECTED_PRINCIPAL": "owner"}
			}
			definition, _ := json.Marshal(def)
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
			if mode == "disabled" || mode == "disabled_os" {
				if err != nil || result.Status != "skipped_disabled" || len(mutations) != 0 {
					t.Fatalf("disabled result %+v %v %v", result, err, mutations)
				}
				return
			}
			if mode == "active" || mode == "wrong_account" || mode == "ambiguous" || mode == "writable" || mode == "symlink" || mode == "old_cli" || mode == "unverifiable_wrapper" || mode == "wrong_pin" {
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

func TestMissingAccountLaunchAgentCreatedOrPreservesDisable(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "disabled"}[disabled], func(t *testing.T) {
			home := t.TempDir()
			cli := filepath.Join(home, "eigenflux")
			if err := os.WriteFile(cli, []byte("fixture"), 0700); err != nil {
				t.Fatal(err)
			}
			task := NotificationTask{CLI: cli, Home: home, Server: "eigenflux", Scope: strings.Repeat("a", 32), AgentID: "agent", PrincipalID: "owner", Dispatch: false}
			label := "ai.eigenflux.watch." + task.Scope
			file := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
			loaded := false
			run := func(_ context.Context, program string, args ...string) ([]byte, error) {
				switch args[0] {
				case "print-disabled":
					if disabled {
						return []byte(`"` + label + `" => true`), nil
					}
					return nil, nil
				case "print":
					if args[1] == "gui/501" || !strings.Contains(args[1], "/ai.") {
						return nil, nil
					}
					if !loaded {
						return nil, errors.New("absent")
					}
					return []byte("\tstate = running\n\tpid = 12345\n"), nil
				case "bootstrap":
					loaded = true
					return nil, nil
				case "kickstart":
					return nil, nil
				default:
					t.Fatalf("unexpected %s %v", program, args)
					return nil, nil
				}
			}
			result, err := startNotificationLaunchAgent(context.Background(), task, home, 501, run)
			if err != nil {
				t.Fatal(err)
			}
			if disabled {
				if result.Status != "skipped_disabled" {
					t.Fatal(result)
				}
				if _, err := os.Stat(file); !os.IsNotExist(err) {
					t.Fatal("disabled task files written")
				}
				return
			}
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if result.PID != 12345 || !strings.Contains(string(raw), "<string>--notifications-only</string>") || !strings.Contains(string(raw), "<key>KeepAlive</key><true/>") || !strings.Contains(string(raw), "<key>EIGENFLUX_WATCH_EXPECTED_AGENT</key><string>agent</string>") {
				t.Fatalf("result=%+v plist=%s", result, raw)
			}
			if err := writeNotificationLaunchAgent(file, task, label); err == nil {
				t.Fatal("existing launcher overwritten")
			}
		})
	}
}
