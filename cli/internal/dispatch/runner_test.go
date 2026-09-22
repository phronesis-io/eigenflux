package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixtureBinding(t *testing.T, mode, scenario string) Binding {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Binding{Mode: mode, Host: "fake", Command: []string{exe, "-test.run=^TestRunnerFixture$", "--"}, Home: t.TempDir(), Server: "bound-server", WorkDir: t.TempDir(), TimeoutSeconds: 5, Env: map[string]string{"EF_RUNNER_FIXTURE": scenario}}
}
func TestRunnerFixture(t *testing.T) {
	scenario := os.Getenv("EF_RUNNER_FIXTURE")
	if scenario == "" {
		return
	}
	switch scenario {
	case "echo":
		b, _ := io.ReadAll(os.Stdin)
		fmt.Print(string(b))
		os.Exit(0)
	case "env":
		fmt.Print(os.Getenv("EIGENFLUX_HOME") + "|" + os.Getenv("EIGENFLUX_SERVER") + "|" + os.Getenv("EIGENFLUX_TOKEN") + "|" + os.Getenv("EIGENFLUX_SKILLS_DIR"))
		os.Exit(0)
	case "environment-boundary":
		values := map[string]string{}
		for _, key := range []string{"EIGENFLUX_HOME", "EIGENFLUX_SERVER", "EIGENFLUX_SKILLS_DIR", "EIGENFLUX_HOST", "EIGENFLUX_CDN_URL", "EIGENFLUX_TOKEN", "EIGENFLUX_ACCESS_TOKEN", "EIGENFLUX_MODEL", "EIGENFLUX_MODE"} {
			values[key] = os.Getenv(key)
		}
		_ = json.NewEncoder(os.Stdout).Encode(values)
		os.Exit(0)
	case "exit":
		fmt.Fprint(os.Stderr, "secret-credential")
		os.Exit(7)
	case "tree":
		exe, _ := os.Executable()
		child := exec.Command(exe, "-test.run=^TestRunnerFixture$", "--")
		child.Env = append(os.Environ(), "EF_RUNNER_FIXTURE=tree-child")
		if child.Start() != nil {
			os.Exit(9)
		}
		time.Sleep(20 * time.Second)
		os.Exit(0)
	case "tree-child":
		time.Sleep(600 * time.Millisecond)
		_ = os.WriteFile(os.Getenv("EF_TREE_MARKER"), []byte("escaped"), 0600)
		os.Exit(0)
	case "timeout":
		time.Sleep(20 * time.Second)
		os.Exit(0)
	case "oversize":
		fmt.Print(strings.Repeat("x", maxRunnerOutput+10))
		os.Exit(0)
	case "acp-script":
		fakeACPScript()
		os.Exit(0)
	default:
		if strings.HasPrefix(scenario, "acp-") {
			fakeACP(scenario)
			os.Exit(0)
		}
	}
	os.Exit(99)
}
func TestArguments(t *testing.T) {
	req := Request{Prompt: "untrusted $(touch /tmp/no)\n--deliver", SessionID: "session-1"}
	cases := []struct {
		host  string
		want  []string
		stdin string
	}{
		{"codex", []string{"fixed", "exec", "--json", "resume", "session-1", "-"}, req.Prompt},
		{"claude-code", []string{"fixed", "--print", "--output-format", "json", "--resume", "session-1"}, req.Prompt},
		{"openclaw", []string{"fixed", "agent", "--local", "--agent", "bound", "--json", "--session-id", "session-1", "--message", req.Prompt}, ""},
		{"hermes", []string{"fixed", "chat", "--quiet", "--resume", "session-1", "-q", req.Prompt}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			r := Runner{Binding: Binding{Mode: "native", Host: tc.host, Args: []string{"fixed"}, HostAgent: "bound"}}
			got, in, err := r.arguments(req)
			if err != nil || !reflect.DeepEqual(got, tc.want) || in != tc.stdin {
				t.Fatalf("args %q stdin %q error %v", got, in, err)
			}
		})
	}
}
func TestNativeResults(t *testing.T) {
	cases := []struct {
		host, data, text, session string
		bad                       bool
	}{
		{"codex", "{\"type\":\"thread.started\",\"thread_id\":\"c1\"}\n{\"type\":\"item.completed\",\"item\":{\"type\":\"command_execution\",\"text\":\"secret\"}}\n{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"answer\"}}\n{\"type\":\"turn.completed\"}\n", "answer", "c1", false},
		{"codex", "{\"type\":\"turn.failed\"}", "", "", true},
		{"codex", "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"answer\"}}", "", "", true},
		{"claude-code", `{"type":"result","subtype":"success","is_error":false,"result":"answer","session_id":"c2"}`, "answer", "c2", false},
		{"claude-code", `{"type":"result","subtype":"success","is_error":true,"result":"API error"}`, "", "", true},
		{"claude-code", `{"type":"result","subtype":"success","permission_denials":[{}],"result":"answer"}`, "", "", true},
		{"openclaw", `{"payloads":[{"text":"answer"}],"meta":{"agentMeta":{"sessionId":"o1"}}}`, "answer", "o1", false},
		{"openclaw", `{"status":"accepted","runId":"r1","result":{"payloads":[{"text":"not final"}]}}`, "", "", true},
		{"openclaw", `{"payloads":[{"text":"partial"}],"meta":{"aborted":true,"agentMeta":{"sessionId":"o1"}}}`, "", "", true},
		{"openclaw", `{"payloads":[{"text":"partial"}],"meta":{"yielded":true,"agentMeta":{"sessionId":"o1"}}}`, "", "", true},
		{"openclaw", `{"payloads":[{"text":"failure","isError":true}],"meta":{"agentMeta":{"sessionId":"o1"}}}`, "", "", true},
		{"openclaw", `{"payloads":[{"text":"failure"}],"meta":{"error":{"message":"failed"},"agentMeta":{"sessionId":"o1"}}}`, "", "", true},
		{"openclaw", `{"payloads":[{"text":"answer"}],"meta":{}}`, "", "", true},
		{"hermes", "answer\n", "answer", "h1", false},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			got, err := parseNative(tc.host, []byte(tc.data), []byte("hidden token\nsession_id: h1\n"))
			if (err != nil) != tc.bad {
				t.Fatalf("error %v", err)
			}
			if !tc.bad && (got.Text != tc.text || got.SessionID != tc.session) {
				t.Fatalf("result %#v", got)
			}
		})
	}
}

func TestOpenClawNewSessionsAreExplicitAndDistinct(t *testing.T) {
	r := Runner{Binding: Binding{Mode: "native", Host: "openclaw", HostAgent: "bound"}}
	sessions := map[string]bool{}
	for range 2 {
		args, _, err := r.arguments(Request{Prompt: "message"})
		if err != nil {
			t.Fatal(err)
		}
		if len(args) != 9 || !reflect.DeepEqual(args[:6], []string{"agent", "--local", "--agent", "bound", "--json", "--session-id"}) {
			t.Fatalf("must use owned local execution and an explicit session: %q", args)
		}
		id := args[6]
		if len(id) != 36 || id[14] != '4' || sessions[id] {
			t.Fatalf("missing unique conversation session: %q", id)
		}
		sessions[id] = true
	}
}
func TestCommandIsolationAndFailures(t *testing.T) {
	t.Setenv("EIGENFLUX_TOKEN", "must-not-inherit")
	t.Setenv("EIGENFLUX_SERVER", "wrong")
	b := fixtureBinding(t, "command", "env")
	b.SkillsDir = t.TempDir()
	b.Env["EIGENFLUX_TOKEN"] = "also-forbidden"
	got, err := (Runner{Binding: b}).Run(context.Background(), Request{})
	if err != nil || got.Text != b.Home+"|bound-server||"+b.SkillsDir {
		t.Fatalf("env %#v %v", got, err)
	}
	b = fixtureBinding(t, "command", "echo")
	prompt := "$(touch unsafe)\n --json"
	got, err = (Runner{Binding: b}).Run(context.Background(), Request{Prompt: prompt})
	if err != nil || got.Text != prompt {
		t.Fatalf("echo %#v %v", got, err)
	}
	for _, scenario := range []string{"exit", "timeout", "oversize"} {
		t.Run(scenario, func(t *testing.T) {
			b := fixtureBinding(t, "command", scenario)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			_, err := (Runner{Binding: b}).Run(ctx, Request{})
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("error %v", err)
			}
			if scenario == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout %v", err)
			}
		})
	}
}
func TestRejectNPMShim(t *testing.T) {
	_, err := (Runner{Binding: Binding{Command: []string{"agent.cmd"}}}).command(nil)
	if err == nil || !strings.Contains(err.Error(), "explicit_node") {
		t.Fatal(err)
	}
}

func TestCancellationKillsDescendants(t *testing.T) {
	b := fixtureBinding(t, "command", "tree")
	marker := filepath.Join(t.TempDir(), "child-survived")
	b.Env["EF_TREE_MARKER"] = marker
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := (Runner{Binding: b}).Run(ctx, Request{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descendant survived cancellation: %v", err)
	}
}

func TestRunnerEnvironmentBoundaryAndCurrentCLI(t *testing.T) {
	t.Setenv("EIGENFLUX_CDN_URL", "http://127.0.0.1:43123/signed-fixture")
	t.Setenv("EIGENFLUX_TOKEN", "must-not-inherit")
	t.Setenv("EIGENFLUX_ACCESS_TOKEN", "must-not-inherit-either")
	t.Setenv("EIGENFLUX_HOST", "codex")
	t.Setenv("CODEX_THREAD_ID", "ambient-codex-marker")
	t.Setenv("EIGENFLUX_MODEL", "ambient-model")
	t.Setenv("EIGENFLUX_MODE", "ambient-mode")
	for _, currentCLI := range []bool{false, true} {
		t.Run(fmt.Sprint(currentCLI), func(t *testing.T) {
			b := fixtureBinding(t, "command", "environment-boundary")
			b.Host = "claude-code"
			b.SkillsDir = t.TempDir()
			b.Env["EIGENFLUX_CDN_URL"] = "https://untrusted-binding.invalid"
			b.Env["EIGENFLUX_TOKEN"] = "binding-token-must-not-inherit"
			var raw []byte
			var err error
			if currentCLI {
				raw, err = RunCommand(context.Background(), b, b.Command, "")
			} else {
				var result Result
				result, err = (Runner{Binding: b}).Run(context.Background(), Request{})
				raw = []byte(result.Text)
			}
			if err != nil {
				t.Fatal(err)
			}
			var values map[string]string
			if err = json.Unmarshal(raw, &values); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"EIGENFLUX_TOKEN", "EIGENFLUX_ACCESS_TOKEN", "EIGENFLUX_MODEL", "EIGENFLUX_MODE"} {
				if values[key] != "" {
					t.Errorf("%s crossed the environment boundary", key)
				}
			}
			wantCDN := ""
			if currentCLI {
				wantCDN = os.Getenv("EIGENFLUX_CDN_URL")
			}
			if values["EIGENFLUX_CDN_URL"] != wantCDN {
				t.Errorf("CDN boundary differs: %q", values["EIGENFLUX_CDN_URL"])
			}
			if values["EIGENFLUX_HOST"] != "claude-code" || values["EIGENFLUX_HOME"] != b.Home || values["EIGENFLUX_SERVER"] != b.Server || values["EIGENFLUX_SKILLS_DIR"] != b.SkillsDir {
				t.Fatal("bound host or account routing was not preserved")
			}
		})
	}
}

func TestRunCommandRejectsOtherExecutables(t *testing.T) {
	b := fixtureBinding(t, "command", "echo")
	other := filepath.Join(t.TempDir(), "other-executable")
	if err := os.WriteFile(other, []byte("isolated fixture, never execute"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{nil, {"relative-cli"}, {other}, {filepath.Join(t.TempDir(), "missing")}} {
		if _, err := RunCommand(context.Background(), b, argv, ""); err == nil || err.Error() != "runner_current_cli_required" {
			t.Fatalf("non-current executable was accepted: argv=%q err=%v", argv, err)
		}
	}
}

func TestNativeBoundHostOverridesAmbientIdentity(t *testing.T) {
	t.Setenv("EIGENFLUX_HOST", "codex")
	for _, host := range []string{"claude-code", "hermes", "openclaw", "codex"} {
		t.Run(host, func(t *testing.T) {
			b := fixtureBinding(t, "native", "echo")
			b.Host = host
			cmd, err := (Runner{Binding: b}).command(nil)
			if err != nil {
				t.Fatal(err)
			}
			found := 0
			for _, entry := range cmd.Env {
				if strings.HasPrefix(entry, "EIGENFLUX_HOST=") {
					found++
					if entry != "EIGENFLUX_HOST="+host {
						t.Fatalf("wrong bound host: %q", entry)
					}
				}
			}
			if found != 1 {
				t.Fatalf("expected one bound host, got %d", found)
			}
		})
	}
}
