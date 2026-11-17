package cmd

import (
	"cli.eigenflux.ai/internal/maintenance"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHeartbeatStopBlocksMissingPublishAndAllowsCompleted(t *testing.T) {
	s := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	c, _ := maintenance.StartCycle(s, "full", []string{"feed", "publish", "need_capture"}, maintenance.RuntimeObservation{})
	p := filepath.Join(t.TempDir(), "transcript.jsonl")
	row := map[string]any{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "turn_id": "turn", "item": map[string]any{"type": "CommandExecution", "aggregated_output": "EIGENFLUX HEARTBEAT PLAN\nheartbeat cycle-report --cycle " + c.ID}}}
	b, _ := json.Marshal(row)
	os.WriteFile(p, append(b, '\n'), 0600)
	in := heartbeatStopInput{SessionID: "owner", TurnID: "turn", Event: "Stop", Transcript: p, LastMessage: "<heartbeat><automation_id>eigenflux</automation_id></heartbeat>"}
	maintenance.ReportCycle(s, c.ID, "feed", "completed")
	d, err := heartbeatStopDecision(s, in, "owner")
	if err != nil || d["decision"] != "block" {
		t.Fatalf("allowed missing stage: %v %v", d, err)
	}
	in.TurnID = "continuation"
	in.StopHookActive = true
	d, err = heartbeatStopDecision(s, in, "owner")
	if err != nil || d["decision"] != "block" {
		t.Fatal("continuation lost the exact open cycle")
	}
	maintenance.ReportCycle(s, c.ID, "publish", "completed")
	d, _ = heartbeatStopDecision(s, in, "owner")
	if d["decision"] != "block" {
		t.Fatal("accepted missing final check")
	}
	if err := maintenance.ReportCycle(s, c.ID, "cycle", "completed"); err != nil {
		t.Fatalf("Need blocked completion: %v", err)
	}
	d, err = heartbeatStopDecision(s, in, "owner")
	if err != nil || len(d) != 0 {
		t.Fatalf("blocked completion: %v %v", d, err)
	}
	in.SessionID = "other"
	in.Transcript = "/missing"
	if d, err = heartbeatStopDecision(s, in, "owner"); err != nil || len(d) != 0 {
		t.Fatal("other chat blocked")
	}
	in.SessionID = "owner"
	in.LastMessage = "普通用户任务"
	in.TurnID = "ordinary"
	in.StopHookActive = false
	if d, err = heartbeatStopDecision(s, in, "owner"); err != nil || len(d) != 0 {
		t.Fatal("ordinary task blocked")
	}
	if id, _ := stopCycleFromTranscript(p, "other-turn"); id != "" {
		t.Fatal("previous turn cleared guard")
	}
}

func TestHeartbeatGuardsDoNotFlushBusinessQueue(t *testing.T) {
	for _, name := range []string{"rules", "cycle-check", "stop-check", "host-observe", "guard-setup", "permission-check"} {
		c, _, err := heartbeatCmd.Find([]string{name})
		if err != nil {
			t.Fatal(err)
		}
		if flushesFeedQueue(c) {
			t.Fatalf("local guard flushes business queue: %s", name)
		}
	}
}

func TestStopGuardCannotBeBypassedByProseAndResumesCheckedCycle(t *testing.T) {
	s := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	c, _ := maintenance.StartCycle(s, "full", []string{"publish"}, maintenance.RuntimeObservation{})
	p := filepath.Join(t.TempDir(), "transcript.jsonl")
	write := func(turn, command, body string) {
		row := map[string]any{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "turn_id": turn, "item": map[string]any{"type": "CommandExecution", "command": []string{"/bin/zsh", "-lc", command}, "aggregated_output": body}}}
		b, _ := json.Marshal(row)
		if err := os.WriteFile(p, append(b, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	in := heartbeatStopInput{SessionID: "owner", TurnID: "turn", Event: "Stop", Transcript: p, LastMessage: "本轮检查尚未完成"}
	write("turn", "eigenflux heartbeat plan --format agent", "EIGENFLUX HEARTBEAT PLAN\nheartbeat cycle-report --cycle "+c.ID)
	d, err := heartbeatStopDecision(s, in, "owner")
	if err != nil || d["decision"] != "block" {
		t.Fatalf("prose bypass: %v %v", d, err)
	}
	in.TurnID = "resume"
	write("resume", "eigenflux heartbeat cycle-check --cycle "+c.ID, "heartbeat incomplete\n"+`{"cycle_id":"`+c.ID+`","status":"incomplete"}`)
	d, err = heartbeatStopDecision(s, in, "owner")
	if err != nil || d["decision"] != "block" {
		t.Fatalf("resume lost cycle: %v %v", d, err)
	}
	write("resume", "eigenflux feed get --item-id 1", `{"cycle_id":"`+c.ID+`","status":"incomplete"}`)
	if id, err := stopCycleFromTranscript(p, "resume"); err != nil || id != "" {
		t.Fatal("untrusted output selected cycle")
	}
}
