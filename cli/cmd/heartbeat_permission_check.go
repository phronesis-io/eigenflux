package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/maintenance"
	"github.com/spf13/cobra"
)

type heartbeatPolicyRunner func(context.Context, string, []string) ([]byte, error)

type heartbeatPermissionEvidence struct {
	Command  []string `json:"command"`
	Decision string   `json:"decision"`
}
type heartbeatPermissionResult struct {
	Status             string                        `json:"status"`
	Checks             []heartbeatPermissionEvidence `json:"checks"`
	ProposedRule       string                        `json:"proposed_rule,omitempty"`
	HostReloadVerified bool                          `json:"host_reload_verified"`
	EvidenceScope      string                        `json:"evidence_scope"`
}

type heartbeatPermissionOptions struct {
	Shell        string
	WatchManaged bool
}

func runHeartbeatPolicy(ctx context.Context, executable string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	// Never expose policy justifications or tool stderr; only parsed decisions are
	// returned to the caller. Bound output from the explicitly selected checker.
	var out policyOutput
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("Codex policy check failed")
	}
	return out.Bytes(), nil
}

type policyOutput struct{ bytes.Buffer }

func (b *policyOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, fmt.Errorf("policy output exceeds size limit")
	}
	return b.Buffer.Write(p)
}

func heartbeatPolicyDecision(ctx context.Context, codex string, rules, command []string, run heartbeatPolicyRunner) (string, error) {
	args := []string{"execpolicy", "check"}
	for _, path := range rules {
		args = append(args, "--rules", path)
	}
	args = append(args, "--")
	args = append(args, command...)
	raw, err := run(ctx, codex, args)
	if err != nil {
		return "unknown", err
	}
	if len(raw) > 1<<20 {
		return "unknown", fmt.Errorf("policy response exceeds size limit")
	}
	var p struct {
		Decision     string            `json:"decision"`
		MatchedRules []json.RawMessage `json:"matchedRules"`
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return "unknown", fmt.Errorf("invalid Codex policy response")
	}
	if len(p.MatchedRules) == 0 {
		return "unknown", nil
	}
	switch p.Decision {
	case "allow", "prompt", "forbidden":
		return p.Decision, nil
	default:
		return "unknown", nil
	}
}

func checkHeartbeatPermission(ctx context.Context, codex string, rules []string, cli, home, server string, run heartbeatPolicyRunner, options ...heartbeatPermissionOptions) (heartbeatPermissionResult, error) {
	result := heartbeatPermissionResult{Status: "unknown", EvidenceScope: "supplied_policy_files_only"}
	if !filepath.IsAbs(codex) || !filepath.IsAbs(cli) || !filepath.IsAbs(home) || server == "" || len(rules) == 0 {
		return result, fmt.Errorf("absolute Codex/CLI/Home, explicit server and active --rules files required")
	}
	for _, arg := range append([]string{codex, cli, home, server}, rules...) {
		if strings.ContainsAny(arg, "\x00\r\n") {
			return result, fmt.Errorf("invalid policy argument")
		}
	}
	for _, path := range rules {
		if !filepath.IsAbs(path) {
			return result, fmt.Errorf("active rule paths must be absolute")
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return result, fmt.Errorf("active rule file is unavailable")
		}
	}
	if len(options) > 1 {
		return result, fmt.Errorf("only one execution configuration may be checked")
	}
	var execution heartbeatPermissionOptions
	if len(options) == 1 {
		execution = options[0]
	}
	launcher, err := heartbeatLauncherArgs(cli, home, server, "skill", execution.Shell)
	if err != nil {
		return result, err
	}
	if execution.WatchManaged {
		launcher = append(launcher, "--watch-managed")
	}
	prefix, err := heartbeatCLIPrefixArgs(cli, home, server, "skill")
	if err != nil {
		return result, err
	}
	prefix = append(prefix, "--origin", publishOriginHeartbeat)
	commands := [][]string{
		launcher,
		append(append([]string{}, prefix...), "feed", "poll", "--batch", "5", "--action", "refresh", "--format", "agent"),
		append(append([]string{}, prefix...), "heartbeat", "cycle-report", "--cycle", strings.Repeat("a", 32), "--stage", "feed", "--result", "completed"),
	}
	allowed, legacy := true, true
	strict := ""
	for _, command := range commands {
		decision, err := heartbeatPolicyDecision(ctx, codex, rules, command, run)
		if err != nil {
			return result, err
		}
		result.Checks = append(result.Checks, heartbeatPermissionEvidence{Command: command, Decision: decision})
		if decision != "allow" {
			allowed = false
		}
		if decision == "forbidden" {
			strict = "forbidden"
		} else if decision == "prompt" && strict != "forbidden" {
			strict = "prompt"
		}
		bare := append([]string{}, command...)
		bare[0] = "eigenflux"
		bareDecision, err := heartbeatPolicyDecision(ctx, codex, rules, bare, run)
		if err != nil {
			return result, err
		}
		if bareDecision != "allow" {
			legacy = false
		}
	}
	switch {
	case strict != "":
		result.Status = strict
	case allowed:
		result.Status = "allowed"
	case legacy:
		result.Status = "legacy_mismatch"
	default:
		result.Status = "unknown"
	}
	if result.Status == "legacy_mismatch" || result.Status == "unknown" {
		tokens, _ := json.Marshal([]string{cli, "--homedir", home, "--server", server})
		result.ProposedRule = "prefix_rule(pattern=" + string(tokens) + ", decision=\"allow\")"
	}
	return result, nil
}

func newHeartbeatPermissionCheckCommand() *cobra.Command {
	command := &cobra.Command{Use: "permission-check", Short: "Check actual Codex rules against the stable launcher and stage commands", Args: cobra.NoArgs}
	command.Flags().String("codex", "", "Absolute owner Codex executable")
	command.Flags().StringArray("rules", nil, "Complete active policy paths; repeat for every applicable rule file")
	command.Flags().String("shell", "auto", "Exact native scheduler shell: auto, posix, powershell or cmd")
	command.Flags().Bool("watch-managed", false, "Check the task's watch-managed companion launcher")
	command.RunE = func(cmd *cobra.Command, _ []string) error {
		codex, _ := cmd.Flags().GetString("codex")
		rules, _ := cmd.Flags().GetStringArray("rules")
		shell, _ := cmd.Flags().GetString("shell")
		managed, _ := cmd.Flags().GetBool("watch-managed")
		cli, err := stableInstalledExecutable()
		if err != nil {
			return err
		}
		scope, err := maintenanceScope(activeServerName())
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
		defer cancel()
		result, err := checkHeartbeatPermission(ctx, codex, rules, cli, scope.Home, scope.Server, runHeartbeatPolicy, heartbeatPermissionOptions{Shell: shell, WatchManaged: managed})
		if runtimeObservationsEnabled() {
			_ = maintenance.RecordLifecycle(scope, maintenance.NewID(), "permission", result.Status, maintenance.RuntimeObservation{Product: "codex", CLI: version})
		}
		if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result); encodeErr != nil {
			return encodeErr
		}
		return err
	}
	return command
}
func init() { heartbeatCmd.AddCommand(newHeartbeatPermissionCheckCommand()) }
