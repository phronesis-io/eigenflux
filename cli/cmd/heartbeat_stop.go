package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"cli.eigenflux.ai/internal/maintenance"
	"github.com/spf13/cobra"
)

type heartbeatStopInput struct {
	SessionID      string `json:"session_id"`
	TurnID         string `json:"turn_id"`
	Event          string `json:"hook_event_name"`
	Transcript     string `json:"transcript_path"`
	LastMessage    string `json:"last_assistant_message"`
	StopHookActive bool   `json:"stop_hook_active"`
}

var cycleInPlan = regexp.MustCompile(`heartbeat cycle-report --cycle ([a-f0-9]{32})`)
var cycleCheckID = regexp.MustCompile(`^[a-f0-9]{32}$`)

type heartbeatCommandItem struct {
	Type    string          `json:"type"`
	Output  string          `json:"aggregated_output"`
	Command json.RawMessage `json:"command"`
	Status  string          `json:"status"`
}

// Read only host-generated command results in the exact current turn. Feed text,
// user messages and previous turns cannot select a cycle to clear the guard.
func visitHeartbeatCommands(path string, acceptTurn func(string) bool, visit func(heartbeatCommandItem)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	const budget = 32 << 20
	if info.Size() > budget {
		if _, err = f.Seek(-budget, io.SeekEnd); err != nil {
			return err
		}
	}
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64<<10), 8<<20)
	if info.Size() > budget {
		scan.Scan()
	}
	for scan.Scan() {
		var row struct {
			Type    string `json:"type"`
			Payload struct {
				Type   string               `json:"type"`
				TurnID string               `json:"turn_id"`
				Item   heartbeatCommandItem `json:"item"`
			} `json:"payload"`
		}
		if json.Unmarshal(scan.Bytes(), &row) != nil {
			continue
		}
		if row.Type != "event_msg" || row.Payload.Type != "item_completed" || !acceptTurn(row.Payload.TurnID) || row.Payload.Item.Type != "CommandExecution" {
			continue
		}
		visit(row.Payload.Item)
	}
	return scan.Err()
}

func heartbeatCycleFromCommands(path string, acceptTurn func(string) bool) (string, error) {
	id := ""
	err := visitHeartbeatCommands(path, acceptTurn, func(item heartbeatCommandItem) {
		out := item.Output
		// Reuse the current main's cycle-check receipt recovery, including resumed
		// turns which have no freshly generated plan.
		if strings.Contains(string(item.Command), "heartbeat cycle-check") {
			for _, line := range strings.Split(out, "\n") {
				var check struct {
					CycleID string `json:"cycle_id"`
					Status  string `json:"status"`
				}
				if json.Unmarshal([]byte(line), &check) == nil && cycleCheckID.MatchString(check.CycleID) {
					switch check.Status {
					case "incomplete", "ready", "completed", "failed":
						id = check.CycleID
					}
				}
			}
			return
		}
		if strings.HasPrefix(out, "{") {
			var plan struct {
				Schema string `json:"schema_version"`
				Prompt string `json:"agent_prompt"`
			}
			if json.Unmarshal([]byte(out), &plan) == nil && plan.Schema == "eigenflux_heartbeat_plan.v1" {
				out = plan.Prompt
			}
		}
		if !strings.HasPrefix(out, "EIGENFLUX HEARTBEAT PLAN\n") {
			return
		}
		if match := cycleInPlan.FindStringSubmatch(out); len(match) == 2 {
			id = match[1]
		}
	})
	return id, err
}

func stopCycleFromTranscript(path, turn string) (string, error) {
	return heartbeatCycleFromCommands(path, func(candidate string) bool { return candidate == turn })
}

func heartbeatCycleForTurn(scope maintenance.Scope, in heartbeatStopInput) (string, error) {
	id, err := stopCycleFromTranscript(in.Transcript, in.TurnID)
	if id != "" {
		return id, err
	}
	marker, ok := readHeartbeatHostMarker(scope, in.SessionID, heartbeatHostAttempt(in.SessionID, in.TurnID))
	if !ok {
		return id, err
	}
	if marker.Cycle != "" {
		return marker.Cycle, nil
	}
	if marker.OriginAttempt != "" && marker.OriginAttempt != heartbeatHostAttempt(in.SessionID, in.TurnID) {
		return heartbeatCycleFromCommands(in.Transcript, func(turn string) bool {
			return heartbeatHostAttempt(in.SessionID, turn) == marker.OriginAttempt
		})
	}
	return id, err
}

// A request for approval is not a refusal. The last command receipt must be a
// final decline of the exact scope; subsequent or ambiguous work clears it.
func heartbeatScopedCommandDeclined(scope maintenance.Scope, in heartbeatStopInput) (bool, error) {
	bin, err := stableInstalledExecutable()
	if err != nil {
		return false, err
	}
	var prefixes []string
	for _, shell := range []string{"posix", "powershell", "cmd"} {
		if prefix, e := renderHeartbeatCLIPrefix(bin, scope.Home, scope.Server, shell, "skill"); e == nil {
			prefixes = append(prefixes, prefix+" ")
		}
	}
	declined := false
	err = visitHeartbeatCommands(in.Transcript, func(turn string) bool { return turn == in.TurnID }, func(item heartbeatCommandItem) {
		declined = strings.EqualFold(item.Status, "declined") && heartbeatCommandIsScoped(item.Command, bin, scope, prefixes)
	})
	return declined, err
}

func heartbeatTargetUnchanged(args []string) bool {
	for _, arg := range args {
		arg = strings.Trim(arg, "'\"")
		if strings.HasPrefix(arg, "--homedir") || strings.HasPrefix(arg, "--server") || strings.HasPrefix(arg, "--runtime-mode") || strings.HasPrefix(arg, "-s") {
			return false
		}
	}
	return len(args) > 0
}

// Native Codex stores either direct argv or one known shell invocation. Do not
// evaluate scripts or accept target overrides and compound shell commands.
func heartbeatCommandIsScoped(raw json.RawMessage, bin string, scope maintenance.Scope, prefixes []string) bool {
	var command string
	if json.Unmarshal(raw, &command) != nil {
		var argv []string
		if json.Unmarshal(raw, &argv) != nil || len(argv) == 0 {
			return false
		}
		expected, err := heartbeatCLIPrefixArgs(bin, scope.Home, scope.Server, "skill")
		if err == nil && len(argv) > len(expected) {
			matches := true
			for i := range expected {
				matches = matches && argv[i] == expected[i]
			}
			if matches {
				return heartbeatTargetUnchanged(argv[len(expected):])
			}
		}
		program := strings.ToLower(filepath.Base(strings.ReplaceAll(argv[0], "\\", "/")))
		switch program {
		case "zsh", "bash", "sh":
			if len(argv) != 3 || (argv[1] != "-c" && argv[1] != "-lc") {
				return false
			}
			command = argv[2]
		case "powershell", "powershell.exe", "pwsh", "pwsh.exe":
			for i := 1; i < len(argv); i++ {
				switch strings.ToLower(argv[i]) {
				case "-nologo", "-noprofile", "-noninteractive":
				case "-command":
					if i != len(argv)-2 {
						return false
					}
					command = argv[i+1]
					i = len(argv)
				default:
					return false
				}
			}
		case "cmd", "cmd.exe":
			for i := 1; i < len(argv); i++ {
				switch strings.ToLower(argv[i]) {
				case "/d", "/s":
				case "/c":
					if i != len(argv)-2 {
						return false
					}
					command = argv[i+1]
					i = len(argv)
				default:
					return false
				}
			}
		default:
			return false
		}
	}
	command = strings.TrimSpace(command)
	for _, prefix := range prefixes {
		if strings.HasPrefix(command, prefix) {
			tail := strings.TrimPrefix(command, prefix)
			shell := "posix"
			if strings.HasPrefix(prefix, "& ") {
				shell = "powershell"
			} else if strings.HasPrefix(prefix, "\"") {
				shell = "cmd"
			}
			args, literal := heartbeatLiteralShellArgs(tail, shell)
			return literal && heartbeatTargetUnchanged(args)
		}
	}
	return false
}

// Accept only literal arguments, with no shell expansion or compound commands.
// Quote concatenation is decoded before checking flags; unsupported escape
// semantics remain unknown instead of establishing another account's refusal.
func heartbeatLiteralShellArgs(text, shell string) ([]string, bool) {
	if strings.ContainsAny(text, "\r\n") || (shell == "cmd" && strings.ContainsAny(text, "%!^")) {
		return nil, false
	}
	var args []string
	var word strings.Builder
	var quote byte
	active := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			if c == quote {
				if shell == "powershell" && quote == '\'' && i+1 < len(text) && text[i+1] == '\'' {
					word.WriteByte('\'')
					i++
					continue
				}
				quote = 0
			} else {
				if quote == '"' && strings.ContainsRune("$`\\", rune(c)) {
					return nil, false
				}
				word.WriteByte(c)
			}
			active = true
			continue
		}
		if c == '\'' || c == '"' {
			quote, active = c, true
			continue
		}
		if c == ' ' || c == '\t' {
			if active {
				args = append(args, word.String())
				word.Reset()
				active = false
			}
			continue
		}
		if strings.ContainsRune("&|;<>`$\\()%!^@*?[]{}~", rune(c)) {
			return nil, false
		}
		word.WriteByte(c)
		active = true
	}
	if quote != 0 {
		return nil, false
	}
	if active {
		args = append(args, word.String())
	}
	return args, len(args) > 0
}

func heartbeatStopDecision(scope maintenance.Scope, in heartbeatStopInput, thread string) (map[string]any, error) {
	allow := map[string]any{}
	if in.SessionID != thread || in.Event != "Stop" || !runtimeObservationsEnabled() {
		return allow, nil
	}
	marked := strings.Contains(in.LastMessage, "<automation_id>eigenflux</automation_id>")
	admitted := heartbeatHostRunAdmitted(scope, in.SessionID, in.TurnID)
	if in.TurnID == "" {
		if !marked {
			return allow, nil
		}
		return nil, fmt.Errorf("missing hook turn identity")
	}
	id, err := heartbeatCycleForTurn(scope, in)
	if err == nil && id == "" && in.StopHookActive {
		if continuation, ok := readHeartbeatHostRun(scope, in.SessionID, heartbeatHostAttempt(in.SessionID, in.TurnID)); ok {
			id = continuation
		} else if marked {
			id, err = maintenance.StopCycle(scope, in.SessionID)
		}
	}
	if id == "" && !marked && !admitted {
		return allow, nil
	}
	if err != nil {
		return nil, err
	}
	_ = maintenance.RecordLifecycle(scope, heartbeatHostAttempt(in.SessionID, in.TurnID), "guard", "active", maintenance.RuntimeObservation{Product: "codex", CLI: observationValue(version)})
	declined, denialErr := heartbeatScopedCommandDeclined(scope, in)
	if denialErr == nil && declined {
		_ = maintenance.RecordLifecycle(scope, heartbeatHostAttempt(in.SessionID, in.TurnID), "permission", "denied", maintenance.RuntimeObservation{Product: "codex", CLI: observationValue(version)})
		if id != "" {
			_ = maintenance.ObserveCycle(scope, id, "permission_denied")
		}
	}
	if id == "" {
		// Plan/authentication failures have no admitted cycle to resume.
		if strings.Contains(in.LastMessage, "<decision>NOTIFY</decision>") {
			return allow, nil
		}
		return map[string]any{"decision": "block", "reason": heartbeatNoPlanReason(heartbeatHostAttempt(in.SessionID, in.TurnID))}, nil
	}
	c, err := maintenance.ReadCycle(scope, id)
	if err != nil {
		return nil, err
	}
	if c.Result == "completed" || c.Result == "failed" {
		verifyUpgradeRepairCycle(scope, in.SessionID, in.TurnID, c)
		return allow, nil
	}
	if strings.Contains(in.LastMessage, "<decision>NOTIFY</decision>") {
		if denialErr == nil && declined {
			// A final host decline may be reported without authorizing the missing
			// command or pretending the open cycle completed.
			_ = maintenance.ObserveCycle(scope, id, "incomplete")
			return allow, nil
		}
	}
	if err := maintenance.BindStopCycle(scope, in.SessionID, id); err != nil {
		return nil, err
	}
	_ = maintenance.ObserveCycle(scope, id, "incomplete")
	rules, stages := maintenance.CycleMissing(c)
	return map[string]any{"decision": "block", "reason": fmt.Sprintf("EigenFlux cycle %s is still open. Pending rule pages: %v. Missing stages: %v. Resume this exact cycle from confirmed receipts; do not repeat completed work or create a replacement plan. Read missing pages with heartbeat rules, execute missing stages, then run heartbeat cycle-check --cycle %s --finish. Report a genuine failed stage and cycle failure only when confirmed.", id, rules, stages, id)}, nil
}

func init() {
	stop := &cobra.Command{Use: "stop-check", Short: "Codex Stop hook: continue an unfinished current-turn EigenFlux cycle", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var in heartbeatStopInput
		if err := json.NewDecoder(io.LimitReader(cmd.InOrStdin(), 1<<20)).Decode(&in); err != nil {
			return err
		}
		thread, _ := cmd.Flags().GetString("thread")
		expected, _ := cmd.Flags().GetString("expected-agent")
		if thread == "" {
			return fmt.Errorf("--thread is required")
		}
		if in.SessionID != thread {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{})
		}
		scope, err := maintenanceScope(activeServerName())
		if err != nil {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(heartbeatStopUnavailableDecision(in))
		}
		if expected != "" && scope.AgentID != expected {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{})
		}
		decision, err := heartbeatStopDecision(scope, in, thread)
		if err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), "[eigenflux:maintenance] component=runtime phase=cycle error_code=local_verification_unavailable")
			decision = map[string]any{"decision": "block", "reason": heartbeatVerificationReason(heartbeatHostAttempt(in.SessionID, in.TurnID))}
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(decision)
	}}
	stop.Flags().String("thread", "", "Exact owner thread; other chats pass through")
	stop.Flags().String("expected-agent", "", "Pinned Agent identity; account switches pass through")
	heartbeatCmd.AddCommand(stop)
}

func heartbeatStopUnavailableDecision(in heartbeatStopInput) map[string]any {
	if strings.Contains(in.LastMessage, "<decision>NOTIFY</decision>") {
		return map[string]any{}
	}
	id, _ := stopCycleFromTranscript(in.Transcript, in.TurnID)
	if id == "" && !strings.Contains(in.LastMessage, "<automation_id>eigenflux</automation_id>") {
		return map[string]any{}
	}
	return map[string]any{"decision": "block", "reason": "EigenFlux completion guard cannot resolve this heartbeat's existing account. Report the concrete account failure to the owner; preserve identity and completed work. Do not create an account or repeat business operations."}
}
