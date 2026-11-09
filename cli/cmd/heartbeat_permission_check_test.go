package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHeartbeatPermissionUsesActualPolicyAndFindsLegacyMismatch(t *testing.T) {
	dir := t.TempDir()
	rules := filepath.Join(dir, "active.rules")
	_ = os.WriteFile(rules, []byte("unchanged policy"), 0600)
	cli := filepath.Join(dir, "eigenflux")
	codex := filepath.Join(dir, "codex")
	tests := []struct{ name, absolute, bare, want string }{
		{"legacy", "", "allow", "legacy_mismatch"}, {"correct", "allow", "", "allowed"}, {"prompt conflict", "prompt", "allow", "prompt"}, {"forbidden conflict", "forbidden", "allow", "forbidden"}, {"no matching policy", "", "", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			run := func(_ context.Context, exe string, args []string) ([]byte, error) {
				calls++
				if exe != codex || len(args) < 6 || args[0] != "execpolicy" || args[1] != "check" || args[2] != "--rules" || args[3] != rules || args[4] != "--" {
					t.Fatalf("wrong checker invocation: %q %q", exe, args)
				}
				decision := tt.absolute
				if args[5] == "eigenflux" {
					decision = tt.bare
				} else if args[5] != cli {
					t.Fatal("not stable absolute executable")
				}
				if decision == "" {
					return []byte(`{"matchedRules":[]}`), nil
				}
				return json.Marshal(map[string]any{"decision": decision, "matchedRules": []any{map[string]string{"decision": decision}}})
			}
			result, err := checkHeartbeatPermission(context.Background(), codex, []string{rules}, cli, dir, "selected", run)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != tt.want || calls != 6 || len(result.Checks) != 3 || result.HostReloadVerified {
				t.Fatalf("wrong evidence: %+v (%d)", result, calls)
			}
			if tt.want == "prompt" || tt.want == "forbidden" {
				if result.ProposedRule != "" {
					t.Fatal("suggested bypassing stricter policy")
				}
			} else if tt.want == "legacy_mismatch" {
				expected, _ := json.Marshal([]string{cli, "--homedir", dir, "--server", "selected"})
				if !strings.Contains(result.ProposedRule, string(expected)) || strings.Contains(result.ProposedRule, `["eigenflux"`) {
					t.Fatal("proposed legacy token")
				}
			}
			raw, _ := os.ReadFile(rules)
			if string(raw) != "unchanged policy" {
				t.Fatal("checker modified policy")
			}
		})
	}
}

func TestHeartbeatPermissionNoUnverifiedAllow(t *testing.T) {
	for _, raw := range []string{`{"decision":"allow","matchedRules":[]}`, `{"decision":"allow"}`, `{"decision":"surprise","matchedRules":[{}]}`} {
		decision, err := heartbeatPolicyDecision(context.Background(), "codex", []string{"rules"}, []string{"command"}, func(context.Context, string, []string) ([]byte, error) { return []byte(raw), nil })
		if err != nil || decision != "unknown" {
			t.Fatalf("invented allowed evidence: %s %v", decision, err)
		}
	}
	_, err := heartbeatPolicyDecision(context.Background(), "codex", nil, nil, func(context.Context, string, []string) ([]byte, error) {
		return nil, errors.New("private stderr must not be printed")
	})
	if err == nil {
		t.Fatal("checker failure ignored")
	}
}

func TestHeartbeatPermissionCombinesEverySuppliedPolicyFile(t *testing.T) {
	dir := t.TempDir()
	one, two := filepath.Join(dir, "one.rules"), filepath.Join(dir, "two,with-comma.rules")
	for _, path := range []string{one, two} {
		if err := os.WriteFile(path, []byte("fixed fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	run := func(_ context.Context, _ string, args []string) ([]byte, error) {
		calls++
		if len(args) < 8 || args[2] != "--rules" || args[3] != one || args[4] != "--rules" || args[5] != two || args[6] != "--" {
			t.Fatalf("did not combine all policies: %q", args)
		}
		return []byte(`{"decision":"forbidden","matchedRules":[{"prefixRuleMatch":{"decision":"allow"}},{"prefixRuleMatch":{"decision":"forbidden"}}]}`), nil
	}
	result, err := checkHeartbeatPermission(context.Background(), filepath.Join(dir, "codex"), []string{one, two}, filepath.Join(dir, "cli"), dir, "server", run)
	if err != nil || result.Status != "forbidden" || calls != 6 || result.ProposedRule != "" {
		t.Fatalf("stricter rule not respected: %+v %v", result, err)
	}
	if result.EvidenceScope != "supplied_policy_files_only" {
		t.Fatal("overclaimed host-loaded policy coverage")
	}
	result, err = checkHeartbeatPermission(context.Background(), filepath.Join(dir, "codex"), nil, filepath.Join(dir, "cli"), dir, "server", run)
	if result.Status != "unknown" || err == nil || calls != 6 {
		t.Fatal("missing policy files manufactured permission evidence")
	}
}

func TestHeartbeatPermissionChecksScheduledShellAndOriginRestrictions(t *testing.T) {
	dir := t.TempDir()
	rules := filepath.Join(dir, "active.rules")
	if err := os.WriteFile(rules, []byte("fixed fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(dir, "eigenflux")
	for _, restriction := range []string{"origin", "shell", "watch-managed"} {
		t.Run(restriction, func(t *testing.T) {
			run := func(_ context.Context, _ string, args []string) ([]byte, error) {
				var command []string
				for i, arg := range args {
					if arg == "--" {
						command = args[i+1:]
						break
					}
				}
				if len(command) < 8 {
					t.Fatalf("incomplete execution command: %q", command)
				}
				decision := "allow"
				if command[0] == cli {
					for i, arg := range command {
						if restriction == "origin" && arg == "--origin" && i+1 < len(command) && command[i+1] == "heartbeat" {
							decision = "forbidden"
						}
						if restriction == "shell" && arg == "--shell" && i+1 < len(command) && command[i+1] == "powershell" {
							decision = "prompt"
						}
						if restriction == "watch-managed" && arg == "--watch-managed" {
							decision = "forbidden"
						}
					}
				}
				return json.Marshal(map[string]any{"decision": decision, "matchedRules": []any{map[string]string{"decision": decision}}})
			}
			result, err := checkHeartbeatPermission(context.Background(), filepath.Join(dir, "codex"), []string{rules}, cli, dir, "selected", run, heartbeatPermissionOptions{Shell: "powershell", WatchManaged: true})
			want := "forbidden"
			if restriction == "shell" {
				want = "prompt"
			}
			if err != nil || result.Status != want || result.ProposedRule != "" {
				t.Fatalf("scheduled execution restriction ignored: %+v %v", result, err)
			}
			launcher, err := heartbeatLauncherArgs(cli, dir, "selected", "skill", "powershell")
			if err != nil {
				t.Fatal(err)
			}
			launcher = append(launcher, "--watch-managed")
			got, _ := json.Marshal(result.Checks[0].Command)
			expected, _ := json.Marshal(launcher)
			if string(got) != string(expected) {
				t.Fatalf("checked launcher differs from actual execution argv: %s != %s", got, expected)
			}
			for _, check := range result.Checks[1:] {
				if len(check.Command) < 9 || check.Command[7] != "--origin" || check.Command[8] != "heartbeat" {
					t.Fatalf("business command lost scheduled origin: %q", check.Command)
				}
			}
		})
	}
}
