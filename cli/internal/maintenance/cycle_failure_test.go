package maintenance

import "testing"

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
	if ReportCycleWithErrorCode(s, c.ID, "feed", "failed", "network_error") == nil {
		t.Fatal("failure reason overwritten")
	}
	if ReportCycleWithErrorCode(s, c.ID, "cycle", "failed", "timeout") == nil {
		t.Fatal("cycle inferred a stage cause")
	}
	if err := ReportCycle(s, c.ID, "cycle", "failed"); err != nil {
		t.Fatal(err)
	}
	if err := ReportCycleWithErrorCode(s, c.ID, "feed", "failed", "timeout"); err != nil {
		t.Fatal(err)
	}
	state, err := Snapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Events) != 4 || state.Cycles[c.ID].FailureCodes["feed"] != "timeout" {
		t.Fatalf("lost or duplicated evidence: %+v", state)
	}
	if state.Events[2].ErrorCode != "timeout" || state.Events[3].ErrorCode != "" {
		t.Fatal("category not scoped to failed stage")
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
