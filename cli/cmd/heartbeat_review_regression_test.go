package cmd

import (
	"cli.eigenflux.ai/internal/maintenance"
	"testing"
)

func TestReviewApprovedRequestDoesNotExcuseMissingStage(t *testing.T) {
	s := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	c, _ := maintenance.StartCycle(s, "full", []string{"feed", "publish"}, maintenance.RuntimeObservation{})
	in := heartbeatHostInput{heartbeatStopInput: heartbeatStopInput{SessionID: "owner", TurnID: "turn", Event: "PermissionRequest", Transcript: heartbeatTranscript(t, "turn", "EIGENFLUX HEARTBEAT PLAN\nheartbeat cycle-report --cycle "+c.ID)}}
	if e := observeHeartbeatHost(s, in, "owner"); e != nil {
		t.Fatal(e)
	}
	// Approval was granted: a real later stage completes, but publish is still missing.
	if e := maintenance.ReportCycle(s, c.ID, "feed", "completed"); e != nil {
		t.Fatal(e)
	}
	in.Event = "Stop"
	in.LastMessage = "<decision>NOTIFY</decision>"
	d, e := heartbeatStopDecision(s, in.heartbeatStopInput, "owner")
	if e != nil || d["decision"] != "block" {
		t.Fatalf("approved request bypassed missing publish: decision=%v err=%v", d, e)
	}
}
func TestReviewContinuationInterruptBelongsToOriginalCycle(t *testing.T) {
	s := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	c, _ := maintenance.StartCycle(s, "full", []string{"feed"}, maintenance.RuntimeObservation{})
	if e := maintenance.BindStopCycle(s, "owner", c.ID); e != nil {
		t.Fatal(e)
	}
	in := heartbeatHostInput{heartbeatStopInput: heartbeatStopInput{SessionID: "owner", TurnID: "continued", Event: "UserPromptSubmit"}, Prompt: "EigenFlux cycle " + c.ID + " is still open. Resume this exact cycle."}
	if e := observeHeartbeatHost(s, in, "owner"); e != nil {
		t.Fatal(e)
	}
	in.Event = "Interrupt"
	in.Transcript = heartbeatTranscript(t, "previous", "unrelated")
	if e := observeHeartbeatHost(s, in, "owner"); e != nil {
		t.Fatal(e)
	}
	st, _ := maintenance.Snapshot(s)
	for _, e := range st.Events {
		if e.AttemptID == c.ID && e.Phase == "cycle" && e.Result == "interrupted" {
			return
		}
	}
	t.Fatal("real continuation interruption was not attributed to the original cycle")
}
func TestReviewTranscriptRecoveryCanFinishQuietly(t *testing.T) {
	s := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	c, _ := maintenance.StartCycle(s, "full", []string{"feed"}, maintenance.RuntimeObservation{})
	in := heartbeatHostInput{heartbeatStopInput: heartbeatStopInput{SessionID: "owner", TurnID: "original", Event: "UserPromptSubmit"}, Prompt: "Run one EigenFlux heartbeat cycle. Execute directly: launcher."}
	in.ExpectedPromptHash = heartbeatTestPromptHash(in.Prompt)
	if e := observeHeartbeatHost(s, in, "owner"); e != nil {
		t.Fatal(e)
	}
	in.Event = "Stop"
	in.Transcript = "/missing"
	if _, e := heartbeatStopDecision(s, in.heartbeatStopInput, "owner"); e == nil {
		t.Fatal("expected initial transcript error")
	}
	in.Prompt = heartbeatVerificationReason(heartbeatHostAttempt("owner", "original"))
	in.Event = "UserPromptSubmit"
	in.TurnID = "continued"
	if e := observeHeartbeatHost(s, in, "owner"); e != nil {
		t.Fatal(e)
	}
	if e := maintenance.ReportCycle(s, c.ID, "feed", "completed"); e != nil {
		t.Fatal(e)
	}
	if e := maintenance.ReportCycle(s, c.ID, "cycle", "completed"); e != nil {
		t.Fatal(e)
	}
	in.Event = "Stop"
	in.StopHookActive = true
	in.LastMessage = "<decision>QUIET</decision>"
	in.Transcript = heartbeatTranscript(t, "original", "EIGENFLUX HEARTBEAT PLAN\nheartbeat cycle-report --cycle "+c.ID)
	d, e := heartbeatStopDecision(s, in.heartbeatStopInput, "owner")
	if e != nil || len(d) != 0 {
		t.Fatalf("successful original-cycle recovery still cannot stop: %v %v", d, e)
	}
}
