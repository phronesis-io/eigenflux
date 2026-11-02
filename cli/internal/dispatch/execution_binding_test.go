package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionBindingRejectsMainOnlyBridgeBeforeLaunch(t *testing.T) {
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	base := validTestBinding(t)
	for _, executable := range []string{filepath.Join(base.Home, "eigenflux"), filepath.Join(base.Home, "EIGENFLUX.EXE"), current} {
		for _, tc := range []struct {
			name string
			argv []string
		}{
			{"direct", []string{"watch", "acp-bridge", "--host", "hermes"}},
			{"root-flags-before", []string{"--homedir", base.Home, "--server", "fixture", "watch", "acp-bridge"}},
			{"root-flags-between", []string{"watch", "--format", "json", "--runtime-mode", "skill", "acp-bridge"}},
			{"inline-root-flags", []string{"--homedir=" + base.Home, "watch", "--server=fixture", "--origin=owner", "acp-bridge"}},
			{"short-root-flags", []string{"-s", "fixture", "watch", "-f", "json", "-v", "acp-bridge"}},
			{"remaining-root-flags", []string{"--no-interactive", "--runtime-model", "fixture-model", "watch", "--verbose=true", "acp-bridge"}},
			{"compact-options", []string{"-sfixture", "watch", "-vfjson", "acp-bridge"}},
			{"bridge-flags-before", []string{"--host", "hermes", "watch", "--owner-url", "ws://127.0.0.1:9000/api/ws", "acp-bridge"}},
			{"bridge-flags-inline", []string{"--main-session-id=existing", "watch", "--workdir", base.WorkDir, "--token-file=/private/token", "acp-bridge"}},
		} {
			for split := 0; split <= len(tc.argv); split++ {
				for _, mode := range []string{"native", "acp", "command"} {
					name := fmt.Sprintf("%s/%s/split-%d/%s", filepath.Base(executable), tc.name, split, mode)
					t.Run(name, func(t *testing.T) {
						binding := base
						binding.Host, binding.Mode = "hermes", mode
						binding.Command = append([]string{executable}, tc.argv[:split]...)
						binding.Args = append([]string{}, tc.argv[split:]...)
						if err := binding.Validate(); !errors.Is(err, ErrMainOnlyExecutionAdapter) {
							t.Fatalf("binding did not identify main-only adapter: %v", err)
						}
						result, err := (Runner{Binding: binding}).Run(context.Background(), Request{Kind: "commission_publish_trial", Prompt: "fixture only"})
						if !errors.Is(err, ErrMainOnlyExecutionAdapter) || result.Text != "" || result.SessionID != "" {
							t.Fatalf("runner launched or hid adapter mismatch: %+v %v", result, err)
						}
					})
				}
			}
		}
	}
}

func TestExecutionBindingHermesNativeKeepsMainBridgeSeparate(t *testing.T) {
	binding := validTestBinding(t)
	binding.Host, binding.Mode = "hermes", "native"
	binding.MainSessionID = "hermes:existing-owner"
	binding.MainCommand = []string{binding.Command[0], "watch", "acp-bridge", "--host", "hermes"}
	if err := binding.Validate(); err != nil {
		t.Fatalf("native Hermes with separate main bridge rejected: %v", err)
	}
	fixture := fixtureBinding(t, "native", "env")
	fixture.Host, fixture.MainSessionID, fixture.MainCommand = binding.Host, binding.MainSessionID, binding.MainCommand
	result, err := (Runner{Binding: fixture}).Run(context.Background(), Request{Kind: "commission_publish_trial", Prompt: "fixture only"})
	if err != nil || !strings.HasPrefix(result.Text, fixture.Home+"|"+fixture.Server+"|") {
		t.Fatalf("native execution was redirected to notification bridge: %+v %v", result, err)
	}
}

func TestExecutionBindingAllowsOrdinaryCommands(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command []string
		args    []string
	}{
		{"third-party-bridge", []string{"/third-party/agent", "watch", "acp-bridge"}, nil},
		{"third-party-args", []string{"/third-party/agent"}, []string{"watch", "acp-bridge"}},
		{"hermes-native", []string{"/third-party/hermes"}, nil},
		{"workbuddy-execution", []string{"eigenflux", "watch", "workbuddy-acp"}, nil},
		{"other-watch-command", []string{"eigenflux", "--server", "acp-bridge", "watch", "status"}, nil},
		{"other-cli-command", []string{"eigenflux", "commission", "publish", "--trial-input", "watch"}, []string{"acp-bridge"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding := Binding{Command: tc.command, Args: tc.args}
			if err := binding.validateExecutionCommand(); err != nil {
				t.Fatalf("ordinary execution command rejected: %v", err)
			}
		})
	}
	fixture := fixtureBinding(t, "command", "echo")
	// A copy is a distinct third-party executable, not an alias of this CLI.
	executable, err := os.ReadFile(fixture.Command[0])
	if err != nil {
		t.Fatal(err)
	}
	thirdParty := filepath.Join(t.TempDir(), "third-party-agent.exe")
	if err := os.WriteFile(thirdParty, executable, 0700); err != nil {
		t.Fatal(err)
	}
	fixture.Command[0] = thirdParty
	fixture.Args = []string{"watch", "acp-bridge"}
	result, err := (Runner{Binding: fixture}).Run(context.Background(), Request{Prompt: "ordinary third-party execution"})
	if err != nil || result.Text != "ordinary third-party execution" {
		t.Fatalf("ordinary execution was changed: %+v %v", result, err)
	}
}
