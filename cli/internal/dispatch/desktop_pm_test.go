package dispatch

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPrivateMessageBlockersSurviveRestartWithoutExposingContent(t *testing.T) {
	b := journalBinding(t)
	j := mustJournal(t, b)
	for _, status := range []string{"needs_user", "no_reply", "replied", "failed", "unknown"} {
		mustAdd(t, j, status)
		job := mustNext(t, j)
		reply := ""
		if status == "replied" {
			reply = "receipt"
		}
		if err := j.Update(job.ID, status, "agent_decision", "session", reply); err != nil {
			t.Fatal(err)
		}
	}
	j = mustJournal(t, b)
	blockers := j.PrivateMessageBlockers()
	if len(blockers) != 1 || blockers[0].Code != "agent_decision" {
		t.Fatal(blockers)
	}
	raw, _ := json.Marshal(blockers)
	if strings.Contains(string(raw), "private body") || strings.Contains(string(raw), "peer") {
		t.Fatal("desktop recovery exposed private content")
	}
	blockers[0].ID = "mutated"
	if j.PrivateMessageBlockers()[0].ID == "mutated" {
		t.Fatal("returned mutable journal state")
	}
}
