package maintenance

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func commissionTestEvent() Event {
	e := NewEvent(strings.Repeat("a", 32), "dispatch", "auto", "commission", "started")
	e.Source, e.Platform = "watch", "darwin"
	e.Commission = &Commission{OrderID: "123", OrderVersion: 2, Role: "seller", JobID: strings.Repeat("b", 64), JobKind: "commission_fulfillment", Stage: "agent", Attempt: 1}
	return e
}

func TestCommissionObservationsUseExistingAccountQueueAndRetry(t *testing.T) {
	scope := Scope{Home: t.TempDir(), Server: "eigenflux", AgentID: "42"}
	e := commissionTestEvent()
	for i := 0; i < 2; i++ {
		if err := Record(scope, e); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := Flush(scope, now, func(events []Event) error {
		if len(events) != 1 || events[0].Commission.OrderID != "123" {
			t.Fatalf("replayed or lost observation: %+v", events)
		}
		return errors.New("offline")
	}); err == nil {
		t.Fatal("offline flush did not retain failure")
	}
	state, err := Snapshot(scope)
	if err != nil || len(state.Events) != 1 {
		t.Fatalf("offline receipt lost: %+v %v", state, err)
	}
	other := scope
	other.AgentID = "43"
	otherState, err := Snapshot(other)
	if err != nil || len(otherState.Events) != 0 {
		t.Fatalf("account observations mixed: %+v %v", otherState, err)
	}
	if err := Flush(scope, now.Add(time.Minute), func(events []Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	state, err = Snapshot(scope)
	if err != nil || len(state.Events) != 0 {
		t.Fatalf("accepted observation retained: %+v %v", state, err)
	}
}

func TestCommissionCodeAndCorrelationAreBounded(t *testing.T) {
	if CommissionCode("credential raw error") != "unknown_failure" || CommissionCode("permission_required") != "permission_required" {
		t.Fatal("unsafe diagnostic classification")
	}
	for name, mutate := range map[string]func(*Event){
		"private run":       func(e *Event) { e.Commission.HostRunIDHash = "raw-session-input" },
		"private code":      func(e *Event) { e.Result, e.ErrorCode = "failed", "token-secret" },
		"cross phase":       func(e *Event) { e.Phase = "notification" },
		"private transport": func(e *Event) { e.Commission.Transport = "https://private-token" },
		"unknown kind":      func(e *Event) { e.Commission.JobKind = "private-kind" },
		"missing read":      func(e *Event) { e.Commission.OrderVersion = 0 },
		"input round":       func(e *Event) { e.Commission.InputRound = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			e := commissionTestEvent()
			mutate(&e)
			if Validate(e) == nil {
				t.Fatal("unsafe observation accepted")
			}
		})
	}
}
