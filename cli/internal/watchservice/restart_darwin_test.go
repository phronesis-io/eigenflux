package watchservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type launchctlTestExit int

func (e launchctlTestExit) Error() string { return "launchctl exit " + strconv.Itoa(int(e)) }
func (e launchctlTestExit) ExitCode() int { return int(e) }

func TestRestartLaunchAgentWaitsForAsynchronousUnload(t *testing.T) {
	home, file, definition := launchFixture(t)
	stopping, started, unloadReads := false, false, 0
	run := func(_ context.Context, program string, args ...string) ([]byte, error) {
		if program == "/usr/bin/plutil" {
			return definition, nil
		}
		if args[0] == "print-disabled" || (args[0] == "print" && strings.Count(args[1], "/") == 1) {
			return nil, nil
		}
		switch args[0] {
		case "print":
			if stopping && !started {
				unloadReads++
				if unloadReads <= 3 {
					return []byte("\tstate = SIGTERMed\n\tpid = 12345\n"), nil
				}
				return nil, launchctlTestExit(113)
			}
			return []byte("\tstate = running\n\tpid = 24680\n"), nil
		case "bootout":
			stopping = true
		case "bootstrap":
			if unloadReads != 4 {
				t.Fatalf("bootstrap before old service disappeared: %d reads", unloadReads)
			}
			started = true
		case "kickstart":
		default:
			t.Fatal(args)
		}
		return nil, nil
	}
	result, err := restartLaunchAgent(context.Background(), file, home, os.Getuid(), run, time.Millisecond, time.Millisecond)
	if err != nil || result.PID != 24680 {
		t.Fatalf("restart: %+v %v", result, err)
	}
}

func TestRestartLaunchAgentDoesNotBootstrapOnUnloadTimeout(t *testing.T) {
	home, file, definition := launchFixture(t)
	stopping := false
	run := func(_ context.Context, program string, args ...string) ([]byte, error) {
		if program == "/usr/bin/plutil" {
			return definition, nil
		}
		switch args[0] {
		case "print-disabled":
			return nil, nil
		case "print":
			return []byte("\tstate = SIGTERMed\n\tpid = 12345\n"), nil
		case "bootout":
			stopping = true
		default:
			t.Fatalf("mutation before unload completed: %v", args)
		}
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := restartLaunchAgent(ctx, file, home, os.Getuid(), run, 0, time.Millisecond)
	if !stopping || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "did not unload") {
		t.Fatalf("timeout: stopping=%t, %v", stopping, err)
	}
}

func TestRestartLaunchAgentDoesNotTreatReadFailuresAsAbsence(t *testing.T) {
	for _, stopping := range []bool{false, true} {
		for _, failure := range []error{exec.ErrNotFound, launchctlTestExit(1), launchctlTestExit(5), context.Canceled} {
			t.Run(strconv.FormatBool(stopping)+"/"+failure.Error(), func(t *testing.T) {
				home, file, definition := launchFixture(t)
				stopped := false
				run := func(_ context.Context, program string, args ...string) ([]byte, error) {
					if program == "/usr/bin/plutil" {
						return definition, nil
					}
					if args[0] == "print-disabled" || (args[0] == "print" && strings.Count(args[1], "/") == 1) {
						return nil, nil
					}
					switch args[0] {
					case "print":
						if !stopping || stopped {
							return nil, failure
						}
						return []byte("\tstate = running\n\tpid = 12345\n"), nil
					case "bootout":
						stopped = true
					default:
						t.Fatalf("mutation after failed state read: %v", args)
					}
					return nil, nil
				}
				_, err := restartLaunchAgent(context.Background(), file, home, os.Getuid(), run, 0, time.Millisecond)
				if !errors.Is(err, failure) || stopped != stopping {
					t.Fatalf("read failure: stopped=%t, %v", stopped, err)
				}
			})
		}
	}
}

func TestRestartLaunchAgentReloadsOnlyExplicitTask(t *testing.T) {
	for _, loaded := range []bool{false, true} {
		t.Run(strconv.FormatBool(loaded), func(t *testing.T) {
			home, file, definition := launchFixture(t)
			var changes []string
			loadedNow := loaded
			run := func(_ context.Context, program string, args ...string) ([]byte, error) {
				if program == "/usr/bin/plutil" {
					return definition, nil
				}
				if program != "/bin/launchctl" {
					t.Fatal(program)
				}
				if args[0] == "print-disabled" || (args[0] == "print" && strings.Count(args[1], "/") == 1) {
					return nil, nil
				}
				switch args[0] {
				case "print":
					if !loadedNow {
						return nil, launchctlTestExit(113)
					}
					return []byte("gui/501/ai.eigenflux.fixture {\n\tstate = running\n\tpid = 24680\n}"), nil
				case "bootout":
					changes = append(changes, strings.Join(args, " "))
					loadedNow = false
				case "bootstrap":
					if loadedNow {
						t.Fatal("did not unload old definition")
					}
					changes = append(changes, strings.Join(args, " "))
					loadedNow = true
				case "kickstart":
					if !loadedNow {
						t.Fatal("started unloaded service")
					}
					changes = append(changes, strings.Join(args, " "))
				default:
					t.Fatal(args)
				}
				return nil, nil
			}
			result, err := restartLaunchAgent(context.Background(), file, home, os.Getuid(), run, time.Millisecond, time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			domain := "gui/" + strconv.Itoa(os.Getuid())
			target := domain + "/ai.eigenflux.fixture"
			want := []string{"bootstrap " + domain + " " + file, "kickstart -p " + target}
			if loaded {
				want = append([]string{"bootout " + target}, want...)
			}
			if !reflect.DeepEqual(changes, want) {
				t.Fatalf("mutations %v, want %v", changes, want)
			}
			if result.Status != "process_started" || result.PID != 24680 || result.Target != target || result.RuntimeVerified {
				t.Fatalf("result: %+v", result)
			}
			if raw, _ := os.ReadFile(file); string(raw) != "fixture" {
				t.Fatal("task definition rewritten")
			}
		})
	}
}

func TestRestartLaunchAgentPreflightPreservesExistingTask(t *testing.T) {
	for _, failure := range []string{"relative", "outside", "symlink", "writable", "wrong_owner", "root", "malformed", "invalid_label", "disabled_plist", "disabled_os", "bad_program", "bad_workdir", "no_gui"} {
		t.Run(failure, func(t *testing.T) {
			home, file, definition := launchFixture(t)
			uid := os.Getuid()
			switch failure {
			case "relative":
				file = "test.plist"
			case "outside":
				file = filepath.Join(home, "test.plist")
			case "symlink":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/dev/null", file); err != nil {
					t.Fatal(err)
				}
			case "writable":
				if err := os.Chmod(file, 0666); err != nil {
					t.Fatal(err)
				}
			case "wrong_owner":
				uid++
			case "root":
				uid = 0
			case "malformed":
				definition = []byte("not-json")
			case "invalid_label":
				definition = []byte(`{"Label":"../other"}`)
			case "disabled_plist":
				definition = []byte(`{"Label":"ai.eigenflux.fixture","Disabled":true}`)
			case "bad_program":
				definition = []byte(`{"Label":"ai.eigenflux.fixture","Program":"/missing-program"}`)
			case "bad_workdir":
				definition = []byte(`{"Label":"ai.eigenflux.fixture","Program":"/bin/sh","WorkingDirectory":"/missing-directory"}`)
			}
			run := func(_ context.Context, program string, args ...string) ([]byte, error) {
				if program == "/usr/bin/plutil" {
					return definition, nil
				}
				if args[0] != "print" && args[0] != "print-disabled" {
					t.Fatalf("mutation before preflight: %v", args)
				}
				if failure == "no_gui" {
					return nil, errors.New("GUI not found")
				}
				if failure == "disabled_os" && args[0] == "print-disabled" {
					return []byte("disabled services = {\n\t\"ai.eigenflux.fixture\" => true\n}"), nil
				}
				return nil, nil
			}
			if _, err := restartLaunchAgent(context.Background(), file, home, uid, run, 0, time.Millisecond); err == nil {
				t.Fatal("invalid preflight accepted")
			}
		})
	}
}

func TestRestartLaunchAgentDoesNotReportLoadedOrCrashLoopAsRunning(t *testing.T) {
	for _, failure := range []string{"bootout", "bootstrap", "kickstart", "missing_pid", "crash_loop"} {
		t.Run(failure, func(t *testing.T) {
			home, file, definition := launchFixture(t)
			started, loaded, pid := false, true, 24000
			run := func(_ context.Context, program string, args ...string) ([]byte, error) {
				if program == "/usr/bin/plutil" {
					return definition, nil
				}
				if args[0] == failure {
					return nil, errors.New("fixture failure")
				}
				if args[0] == "kickstart" {
					started = true
				}
				if args[0] == "bootout" {
					loaded = false
				}
				if args[0] == "bootstrap" {
					loaded = true
				}
				if args[0] == "print" && strings.Count(args[1], "/") == 2 && !loaded {
					return nil, launchctlTestExit(113)
				}
				if args[0] == "print" && strings.Count(args[1], "/") == 2 && started {
					if failure == "crash_loop" {
						pid++
						return []byte("\tstate = running\n\tpid = " + strconv.Itoa(pid) + "\n"), nil
					}
					return []byte("\tstate = waiting\n\tlast exit code = 1\n"), nil
				}
				return nil, nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			result, err := restartLaunchAgent(ctx, file, home, os.Getuid(), run, 10*time.Millisecond, time.Millisecond)
			if err == nil || result.Status != "" {
				t.Fatalf("false success: %+v %v", result, err)
			}
		})
	}
}

func launchFixture(t *testing.T) (string, string, []byte) {
	t.Helper()
	home := t.TempDir()
	file := filepath.Join(home, "Library", "LaunchAgents", "fixture with spaces.plist")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	definition, err := json.Marshal(map[string]any{"Label": "ai.eigenflux.fixture", "ProgramArguments": []string{"/bin/sh", "custom wrapper with spaces.sh", "--original-account"}, "WorkingDirectory": home})
	if err != nil {
		t.Fatal(err)
	}
	return home, file, definition
}
