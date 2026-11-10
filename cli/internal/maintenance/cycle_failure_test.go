package maintenance

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestFailedStageCategoryIsDurableIdempotentAndCannotBeRewritten(t *testing.T) {
	s := Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	c, err := StartCycle(s, "full", []string{"feed"}, RuntimeObservation{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := ReportCycleWithErrorCode(s, c.ID, "feed", "failed", "timeout"); err != nil {
			t.Fatal(err)
		}
	}
	if err := ReportCycle(s, c.ID, "feed", "failed"); err != nil {
		t.Fatal(err)
	}
	if ReportCycleWithErrorCode(s, c.ID, "feed", "failed", "network_error") == nil {
		t.Fatal("failure reason overwritten")
	}
	if ReportCycleWithErrorCode(s, c.ID, "cycle", "failed", "timeout") == nil {
		t.Fatal("cycle inferred a stage cause")
	}
	if err := ReportCycle(s, c.ID, "cycle", "failed"); err != nil {
		t.Fatal(err)
	}
	if err := ReportCycle(s, c.ID, "feed", "failed"); err != nil {
		t.Fatal(err)
	}
	if err := ReportCycleWithErrorCode(s, c.ID, "feed", "failed", "timeout"); err != nil {
		t.Fatal(err)
	}
	var eventID string
	if err := Flush(s, time.Now(), func(events []Event) error {
		if len(events) != 4 || events[2].ErrorCode != "timeout" || events[3].ErrorCode != "" {
			t.Fatalf("unexpected upload: %+v", events)
		}
		eventID = events[2].EventID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Simulate an older CLI rewriting the state after the queue was acknowledged.
	var legacy struct {
		Attempts map[string]Event `json:"attempts"`
		Cycles   map[string]struct {
			ID                string            `json:"id"`
			StartedAt         int64             `json:"started_at"`
			Mode              string            `json:"mode"`
			Stages            map[string]string `json:"stages"`
			Result            string            `json:"result"`
			ExecutionObserved bool              `json:"execution_observed"`
		} `json:"cycles"`
	}
	path, err := s.path()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ReportCycleWithErrorCode(s, c.ID, "feed", "failed", "timeout"); err != nil {
		t.Fatal(err)
	}
	if ReportCycleWithErrorCode(s, c.ID, "feed", "failed", "network_error") == nil {
		t.Fatal("failure category rewritten after a legacy state write")
	}
	state, err := Snapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Events) != 0 || state.Attempts[stageFailureAttemptPrefix+eventID].ErrorCode != "timeout" {
		t.Fatalf("lost or duplicated evidence: %+v", state)
	}
	if err := update(s, func(st *State) error {
		delete(st.Cycles, c.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	state, err = Snapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Attempts[stageFailureAttemptPrefix+eventID]; ok {
		t.Fatal("category outlived its cycle")
	}
}

func TestRuntimeFailureCategoriesNeverAcceptRawErrorsOrSuccessfulStages(t *testing.T) {
	for _, code := range []string{"authentication_failed", "permission_denied", "timeout", "network_error", "rate_limited", "server_error", "host_unavailable", "unknown_failure"} {
		e := NewEvent(NewID(), "runtime", "auto", "cycle", "failed")
		e.Runtime = &RuntimeObservation{Stage: "feed", PlanMode: "full"}
		e.ErrorCode = code
		if err := Validate(e); err != nil {
			t.Fatal(err)
		}
		e.Result = "completed"
		if Validate(e) == nil {
			t.Fatal("successful stage has failure category")
		}
	}
	for _, code := range []string{"HTTP 401 bearer secret", "custom_private_code", "/Users/private"} {
		e := NewEvent(NewID(), "runtime", "auto", "cycle", "failed")
		e.Runtime = &RuntimeObservation{Stage: "feed", PlanMode: "full"}
		e.ErrorCode = code
		if Validate(e) == nil {
			t.Fatal("raw or unknown error accepted")
		}
	}
}
