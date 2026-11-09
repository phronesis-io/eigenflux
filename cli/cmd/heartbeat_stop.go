package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
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

// Read only host-generated command results in the exact current turn. Feed text,
// user messages and previous turns cannot select a cycle to clear the guard.
func stopCycleFromTranscript(path, turn string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	const budget = 32 << 20
	if info.Size() > budget {
		if _, err = f.Seek(-budget, io.SeekEnd); err != nil {
			return "", err
		}
	}
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64<<10), 8<<20)
	if info.Size() > budget {
		scan.Scan()
	}
	id := ""
	for scan.Scan() {
		var row struct {
			Type    string `json:"type"`
			Payload struct {
				Type   string `json:"type"`
				TurnID string `json:"turn_id"`
				Item   struct {
					Type   string `json:"type"`
					Output string `json:"aggregated_output"`
				} `json:"item"`
			} `json:"payload"`
		}
		if json.Unmarshal(scan.Bytes(), &row) != nil {
			continue
		}
		if row.Type != "event_msg" || row.Payload.Type != "item_completed" || row.Payload.TurnID != turn || row.Payload.Item.Type != "CommandExecution" {
			continue
		}
		out := row.Payload.Item.Output
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
			continue
		}
		if match := cycleInPlan.FindStringSubmatch(out); len(match) == 2 {
			id = match[1]
		}
	}
	return id, scan.Err()
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
	id, err := stopCycleFromTranscript(in.Transcript, in.TurnID)
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
		return allow, nil
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
