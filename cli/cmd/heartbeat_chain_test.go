package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/maintenance"
)

func heartbeatTestPromptHash(prompt string) string {
	s := sha256.Sum256([]byte(strings.TrimSpace(prompt)))
	return hex.EncodeToString(s[:])
}

func heartbeatTranscript(t *testing.T, turn, output string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "transcript.jsonl")
	row := map[string]any{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "turn_id": turn, "item": map[string]any{"type": "CommandExecution", "aggregated_output": output}}}
	b, _ := json.Marshal(row)
	if err := os.WriteFile(p, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

type heartbeatBrokenWriter struct{}

func (heartbeatBrokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken output") }

func TestHeartbeatPlanDeliverySeparatesPlanningFromExecution(t *testing.T) {
	for _, format := range []string{"agent", "json"} {
		t.Run(format, func(t *testing.T) {
			scope := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
			c, err := maintenance.StartCycle(scope, "full", []string{"feed"}, maintenance.RuntimeObservation{})
			if err != nil {
				t.Fatal(err)
			}
			p := heartbeatPlan{CycleID: c.ID, AgentPrompt: "EIGENFLUX HEARTBEAT PLAN\nheartbeat cycle-report --cycle " + c.ID, SchemaVersion: "eigenflux_heartbeat_plan.v1"}
			var diagnostic, out bytes.Buffer
			if err := deliverHeartbeatPlan(heartbeatBrokenWriter{}, &diagnostic, format, p, scope); err == nil {
				t.Fatal("failed output accepted")
			}
			st, _ := maintenance.Snapshot(scope)
			for _, event := range st.Events {
				if event.Result == "plan_created" {
					t.Fatal("undelivered plan recorded")
				}
			}
			if err := deliverHeartbeatPlan(&out, &diagnostic, format, p, scope); err != nil {
				t.Fatal(err)
			}
			if id, err := stopCycleFromTranscript(heartbeatTranscript(t, "turn", out.String()), "turn"); err != nil || id != c.ID {
				t.Fatalf("plan format lost guard identity: %s %v", id, err)
			}
			st, _ = maintenance.Snapshot(scope)
			n := 0
			for _, event := range st.Events {
				if event.Result == "plan_created" {
					n++
				}
				if event.Result == "started" {
					t.Fatal("plan marked executed")
				}
			}
			if n != 1 || st.Cycles[c.ID].Result != "" {
				t.Fatalf("planning changed cycle: %+v", st)
			}
		})
	}
}

func TestHeartbeatStopCannotBypassGuardByOmittingXML(t *testing.T) {
	scope := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	c, _ := maintenance.StartCycle(scope, "full", []string{"feed"}, maintenance.RuntimeObservation{})
	p := heartbeatTranscript(t, "turn", "EIGENFLUX HEARTBEAT PLAN\nheartbeat cycle-report --cycle "+c.ID)
	in := heartbeatStopInput{SessionID: "owner", TurnID: "turn", Event: "Stop", Transcript: p, LastMessage: "已完成"}
	d, err := heartbeatStopDecision(scope, in, "owner")
	if err != nil || d["decision"] != "block" {
		t.Fatalf("missing XML bypassed incomplete cycle: %v %v", d, err)
	}
	// A different ordinary turn in the same chat must not inherit the old cycle.
	in.TurnID, in.StopHookActive = "ordinary", true
	d, err = heartbeatStopDecision(scope, in, "owner")
	if err != nil || len(d) != 0 {
		t.Fatalf("unrelated continuation revived old cycle: %v %v", d, err)
	}
}

func TestHeartbeatHostEvidenceAdmissionPermissionAndInterrupt(t *testing.T) {
	scope := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	in := heartbeatHostInput{heartbeatStopInput: heartbeatStopInput{SessionID: "owner", TurnID: "run", Event: "UserPromptSubmit"}, Prompt: "Run one EigenFlux heartbeat cycle. Execute directly: scoped launcher. private text"}
	in.ExpectedPromptHash = heartbeatTestPromptHash(in.Prompt)
	if err := observeHeartbeatHost(scope, in, "other"); err != nil {
		t.Fatal(err)
	}
	st, _ := maintenance.Snapshot(scope)
	if len(st.Events) != 0 {
		t.Fatal("another owner admitted")
	}
	if err := observeHeartbeatHost(scope, in, "owner"); err != nil {
		t.Fatal(err)
	}
	in.Event = "PermissionRequest"
	if err := observeHeartbeatHost(scope, in, "owner"); err != nil {
		t.Fatal(err)
	}
	in.Event = "Interrupt"
	if err := observeHeartbeatHost(scope, in, "owner"); err != nil {
		t.Fatal(err)
	}
	st, _ = maintenance.Snapshot(scope)
	seen := map[string]bool{}
	for _, event := range st.Events {
		seen[event.Runtime.Stage+":"+event.Result] = true
	}
	for _, want := range []string{"trigger:observed", "guard:active", "permission:required", "execution:interrupted"} {
		if !seen[want] {
			t.Fatal(want)
		}
	}
	b, _ := json.Marshal(st.Events)
	if bytes.Contains(b, []byte("private text")) || bytes.Contains(b, []byte("owner")) {
		t.Fatal("raw host data uploaded")
	}
	// Trigger admission is fenced by account, even for the same owner and turn.
	other := scope
	other.AgentID = "2"
	if heartbeatHostRunAdmitted(other, "owner", "run") {
		t.Fatal("admission followed account switch")
	}
}

func TestHeartbeatHostQuotedTemplateIsNotAScheduledRun(t *testing.T) {
	scope := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	canonical := "Run one EigenFlux heartbeat cycle. Execute directly: exact scoped launcher."
	in := heartbeatHostInput{heartbeatStopInput: heartbeatStopInput{SessionID: "owner", TurnID: "ordinary", Event: "UserPromptSubmit"}, Prompt: "解释这句 " + canonical, ExpectedPromptHash: heartbeatTestPromptHash(canonical)}
	if err := observeHeartbeatHost(scope, in, "owner"); err != nil {
		t.Fatal(err)
	}
	if heartbeatHostRunAdmitted(scope, "owner", "ordinary") {
		t.Fatal("quoted scheduler template admitted")
	}
	in.Event = "Stop"
	if d, err := heartbeatStopDecision(scope, in.heartbeatStopInput, "owner"); err != nil || len(d) != 0 {
		t.Fatalf("ordinary quoted template blocked: %v %v", d, err)
	}
}

func TestHeartbeatNoPlanContinuationCannotReuseOldCompletedCycle(t *testing.T) {
	scope := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	c, _ := maintenance.StartCycle(scope, "full", []string{"feed"}, maintenance.RuntimeObservation{})
	_ = maintenance.ReportCycle(scope, c.ID, "feed", "completed")
	_ = maintenance.ReportCycle(scope, c.ID, "cycle", "completed")
	_ = maintenance.BindStopCycle(scope, "owner", c.ID)
	in := heartbeatHostInput{heartbeatStopInput: heartbeatStopInput{SessionID: "owner", TurnID: "new-run", Event: "UserPromptSubmit"}, Prompt: "Run one EigenFlux heartbeat cycle. Execute directly: scoped launcher."}
	in.ExpectedPromptHash = heartbeatTestPromptHash(in.Prompt)
	if err := observeHeartbeatHost(scope, in, "owner"); err != nil {
		t.Fatal(err)
	}
	in.Event, in.Transcript = "Stop", heartbeatTranscript(t, "other", "unrelated")
	d, err := heartbeatStopDecision(scope, in.heartbeatStopInput, "owner")
	if err != nil || d["decision"] != "block" {
		t.Fatalf("missing plan passed: %v %v", d, err)
	}
	in.Event, in.TurnID, in.Prompt = "UserPromptSubmit", "continued", d["reason"].(string)
	if err := observeHeartbeatHost(scope, in, "owner"); err != nil {
		t.Fatal(err)
	}
	if !heartbeatHostRunAdmitted(scope, "owner", "continued") {
		t.Fatal("no-plan continuation lost its owner run")
	}
	in.Event, in.StopHookActive = "Stop", true
	d, err = heartbeatStopDecision(scope, in.heartbeatStopInput, "owner")
	if err != nil || d["decision"] != "block" {
		t.Fatalf("old completed cycle cleared new run: %v %v", d, err)
	}
}

func TestHeartbeatHostContinuationKeepsCycleWithoutInventingTrigger(t *testing.T) {
	scope := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	c, _ := maintenance.StartCycle(scope, "full", []string{"feed"}, maintenance.RuntimeObservation{})
	if err := maintenance.BindStopCycle(scope, "owner", c.ID); err != nil {
		t.Fatal(err)
	}
	in := heartbeatHostInput{heartbeatStopInput: heartbeatStopInput{SessionID: "owner", TurnID: "continued", Event: "UserPromptSubmit"}, Prompt: "EigenFlux cycle " + c.ID + " is still open. Resume this exact cycle."}
	if err := observeHeartbeatHost(scope, in, "owner"); err != nil {
		t.Fatal(err)
	}
	if !heartbeatHostRunAdmitted(scope, "owner", "continued") {
		t.Fatal("continuation lost admission")
	}
	in.Event, in.StopHookActive = "Stop", true
	in.Transcript = heartbeatTranscript(t, "ordinary", "unrelated")
	d, err := heartbeatStopDecision(scope, in.heartbeatStopInput, "owner")
	if err != nil || d["decision"] != "block" {
		t.Fatalf("continuation lost cycle: %v %v", d, err)
	}
	st, _ := maintenance.Snapshot(scope)
	for _, event := range st.Events {
		if event.Phase == "lifecycle" && event.Runtime.Stage == "trigger" {
			t.Fatal("continuation counted as new scheduler trigger")
		}
	}
}

func TestHeartbeatHooksHonorObservationOptOutAndOrdinaryAccountRemoval(t *testing.T) {
	scope := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	t.Setenv("EIGENFLUX_RUNTIME_OBSERVATIONS", "0")
	in := heartbeatHostInput{heartbeatStopInput: heartbeatStopInput{SessionID: "owner", TurnID: "turn", Event: "UserPromptSubmit"}, Prompt: "Run one EigenFlux heartbeat cycle. Execute directly: launcher"}
	if err := observeHeartbeatHost(scope, in, "owner"); err != nil {
		t.Fatal(err)
	}
	st, _ := maintenance.Snapshot(scope)
	if len(st.Events) != 0 || heartbeatHostRunAdmitted(scope, "owner", "turn") {
		t.Fatal("observation opt-out ignored")
	}
	in.Event, in.LastMessage = "Stop", "<automation_id>eigenflux</automation_id>"
	if d, err := heartbeatStopDecision(scope, in.heartbeatStopInput, "owner"); err != nil || len(d) != 0 {
		t.Fatalf("opt-out broke existing execution: %v %v", d, err)
	}
	ordinary := heartbeatStopInput{Event: "Stop", TurnID: "ordinary", Transcript: "/missing", LastMessage: "ordinary answer"}
	if d := heartbeatStopUnavailableDecision(ordinary); len(d) != 0 {
		t.Fatal("removed account blocked ordinary task")
	}
	ordinary.LastMessage = "<automation_id>eigenflux</automation_id>"
	if d := heartbeatStopUnavailableDecision(ordinary); d["decision"] != "block" {
		t.Fatal("heartbeat account failure silently accepted")
	}
	ordinary.LastMessage += "<decision>NOTIFY</decision>"
	if d := heartbeatStopUnavailableDecision(ordinary); len(d) != 0 {
		t.Fatal("account failure notification trapped in guard loop")
	}
}

func TestHeartbeatPermissionBarrierCanNotifyWithoutFakingCompletion(t *testing.T) {
	scope := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	c, _ := maintenance.StartCycle(scope, "full", []string{"feed"}, maintenance.RuntimeObservation{})
	p := heartbeatTranscript(t, "turn", "EIGENFLUX HEARTBEAT PLAN\nheartbeat cycle-report --cycle "+c.ID)
	in := heartbeatHostInput{heartbeatStopInput: heartbeatStopInput{SessionID: "owner", TurnID: "turn", Event: "Stop", Transcript: p, LastMessage: "<decision>NOTIFY</decision>"}}
	if d, err := heartbeatStopDecision(scope, in.heartbeatStopInput, "owner"); err != nil || d["decision"] != "block" {
		t.Fatalf("notification alone bypassed unfinished work: %v %v", d, err)
	}
	in.Event = "PermissionRequest"
	if err := observeHeartbeatHost(scope, in, "owner"); err != nil {
		t.Fatal(err)
	}
	in.Event = "Stop"
	if d, err := heartbeatStopDecision(scope, in.heartbeatStopInput, "owner"); err != nil || d["decision"] != "block" {
		t.Fatalf("approval request without final refusal bypassed unfinished work: %v %v", d, err)
	}
	prefix, err := nativeHeartbeatCLIPrefix(scope.Home, scope.Server, "posix", "skill")
	if err != nil {
		t.Fatal(err)
	}
	appendHeartbeatHostCommand(t, p, "turn", prefix+" --origin heartbeat feed poll", "declined", "")
	if d, err := heartbeatStopDecision(scope, in.heartbeatStopInput, "owner"); err != nil || len(d) != 0 {
		t.Fatalf("final scoped refusal trapped owner notification: %v %v", d, err)
	}
	appendHeartbeatHostCommand(t, p, "turn", prefix+" --origin heartbeat feed poll", "completed", "success")
	if d, err := heartbeatStopDecision(scope, in.heartbeatStopInput, "owner"); err != nil || d["decision"] != "block" {
		t.Fatalf("later success did not clear old refusal: %v %v", d, err)
	}
	current, _ := maintenance.ReadCycle(scope, c.ID)
	if current.Result != "" || current.Stages["feed"] != "" {
		t.Fatal("notification rewrote cycle execution or completion")
	}
}

func appendHeartbeatHostCommand(t *testing.T, path, turn, command, status, output string) {
	appendHeartbeatHostCommandValue(t, path, turn, command, status, output)
}

func appendHeartbeatHostCommandValue(t *testing.T, path, turn string, command any, status, output string) {
	t.Helper()
	row := map[string]any{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "turn_id": turn, "item": map[string]any{"type": "CommandExecution", "command": command, "status": status, "aggregated_output": output}}}
	b, _ := json.Marshal(row)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

func TestHeartbeatNativeDeclineArgvAndScopeFence(t *testing.T) {
	scope := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	bin, err := stableInstalledExecutable()
	if err != nil {
		t.Fatal(err)
	}
	prefixes := []string{}
	for _, shell := range []string{"posix", "powershell", "cmd"} {
		prefix, err := renderHeartbeatCLIPrefix(bin, scope.Home, scope.Server, shell, "skill")
		if err != nil {
			t.Fatal(err)
		}
		prefixes = append(prefixes, prefix+" ")
	}
	args, _ := heartbeatCLIPrefixArgs(bin, scope.Home, scope.Server, "skill")
	direct := append(args, "--origin", "heartbeat", "feed", "poll")
	posix := prefixes[0] + "--origin heartbeat feed poll"
	for _, tc := range []struct {
		name    string
		command any
		want    bool
	}{
		{"native zsh", []string{"/bin/zsh", "-lc", posix}, true},
		{"direct argv", direct, true},
		{"native powershell", []string{"powershell.exe", "-NoLogo", "-NoProfile", "-Command", prefixes[1] + "--origin heartbeat feed poll"}, true},
		{"native cmd", []string{"cmd.exe", "/d", "/s", "/c", prefixes[2] + "--origin heartbeat feed poll"}, true},
		{"target override", []string{"/bin/zsh", "-lc", posix + " --server other"}, false},
		{"short target override", []string{"/bin/zsh", "-lc", posix + " -sother"}, false},
		{"direct target override", append(append([]string{}, direct...), "--homedir", "/other"), false},
		{"compound command", []string{"/bin/zsh", "-lc", posix + " && echo ok"}, false},
		{"unknown wrapper", []string{"python3", "-c", posix}, false},
		{"different target", []string{"/bin/zsh", "-lc", strings.Replace(posix, shellQuote(scope.Server), shellQuote("other"), 1)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.command)
			if got := heartbeatCommandIsScoped(raw, bin, scope, prefixes); got != tc.want {
				t.Fatalf("scoped=%v want=%v", got, tc.want)
			}
		})
	}
	c, _ := maintenance.StartCycle(scope, "full", []string{"feed"}, maintenance.RuntimeObservation{})
	p := heartbeatTranscript(t, "turn", "EIGENFLUX HEARTBEAT PLAN\nheartbeat cycle-report --cycle "+c.ID)
	appendHeartbeatHostCommandValue(t, p, "turn", []string{"/bin/zsh", "-lc", posix}, "declined", "")
	in := heartbeatStopInput{SessionID: "owner", TurnID: "turn", Event: "Stop", Transcript: p, LastMessage: "<decision>NOTIFY</decision>"}
	if d, err := heartbeatStopDecision(scope, in, "owner"); err != nil || len(d) != 0 {
		t.Fatalf("native final refusal lost: %v %v", d, err)
	}
	appendHeartbeatHostCommandValue(t, p, "turn", []string{"/bin/zsh", "-lc", "echo ambiguous later work"}, "completed", "")
	if d, err := heartbeatStopDecision(scope, in, "owner"); err != nil || d["decision"] != "block" {
		t.Fatalf("later ambiguous work retained old refusal: %v %v", d, err)
	}
}
