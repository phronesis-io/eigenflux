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

var cycleCheckID = regexp.MustCompile(`^[a-f0-9]{32}$`)

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
					Type    string          `json:"type"`
					Output  string          `json:"aggregated_output"`
					Command json.RawMessage `json:"command"`
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
		if strings.Contains(string(row.Payload.Item.Command), "heartbeat cycle-check") {
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
			continue
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
	if in.SessionID != thread || in.Event != "Stop" {
		return allow, nil
	}
	marked := strings.Contains(in.LastMessage, "<automation_id>eigenflux</automation_id>")
	if in.TurnID == "" {
		if !marked {
			return allow, nil
		}
		return nil, fmt.Errorf("missing hook turn identity")
	}
	id, err := stopCycleFromTranscript(in.Transcript, in.TurnID)
	if err == nil && id == "" && in.StopHookActive {
		id, err = maintenance.StopCycle(scope, in.SessionID)
	}
	if id == "" && !marked && !in.StopHookActive {
		return allow, nil
	}
	if err != nil {
		return nil, err
	}
	if id == "" {
		// Plan/authentication failures have no admitted cycle to resume.
		if strings.Contains(in.LastMessage, "<decision>NOTIFY</decision>") {
			return allow, nil
		}
		return map[string]any{"decision": "block", "reason": "EigenFlux heartbeat has no verified current-turn cycle. Run its scoped heartbeat plan, execute the plan, and run cycle-check --finish before returning."}, nil
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
		if thread == "" {
			return fmt.Errorf("--thread is required")
		}
		if in.SessionID != thread {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{})
		}
		scope, err := maintenanceScope(activeServerName())
		if err != nil {
			return err
		}
		decision, err := heartbeatStopDecision(scope, in, thread)
		if err != nil {
			decision = map[string]any{"decision": "block", "reason": "EigenFlux completion guard could not verify the current cycle: " + err.Error() + ". Resolve this local verification failure before finishing; do not replay business mutations."}
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(decision)
	}}
	stop.Flags().String("thread", "", "Exact owner thread; other chats pass through")
	heartbeatCmd.AddCommand(stop)
}
