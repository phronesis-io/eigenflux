package dispatch

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommissionClarificationDecisionContract(t *testing.T) {
	job := CommissionClarificationJob{Version: 1, OrderID: "901", OrderVersion: 4, BuyerID: commissionTestAgent, RequestID: strings.Repeat("a", 64), Round: 1}
	good := CommissionClarificationDecision{Version: 1, RequestID: "job", OrderID: "901", OrderVersion: 4, InputRequestID: job.RequestID, Outcome: "answered", Answer: "Use the original specified audience", Summary: "Known buyer fact"}
	for _, scenario := range []string{"answered", "waiting", "wrong_job", "wrong_order", "wrong_version", "wrong_request", "unknown_action", "waiting_answer", "empty_answer", "extra_field", "too_long"} {
		t.Run(scenario, func(t *testing.T) {
			decision := good
			switch scenario {
			case "waiting":
				decision.Outcome, decision.Answer = "waiting", ""
			case "wrong_job":
				decision.RequestID = "other"
			case "wrong_order":
				decision.OrderID = "902"
			case "wrong_version":
				decision.OrderVersion++
			case "wrong_request":
				decision.InputRequestID = strings.Repeat("b", 64)
			case "unknown_action":
				decision.Outcome = "needs_user"
			case "waiting_answer":
				decision.Outcome = "waiting"
			case "empty_answer":
				decision.Answer = ""
			case "too_long":
				decision.Answer = strings.Repeat("a", 8001)
			}
			raw, _ := json.Marshal(decision)
			if scenario == "extra_field" {
				raw = append(raw[:len(raw)-1], []byte(`,"command":"write"}`)...)
			}
			_, err := ParseCommissionClarificationDecision(string(raw), "job", job)
			if (err == nil) != (scenario == "answered" || scenario == "waiting") {
				t.Fatalf("unsafe contract: %s %v", scenario, err)
			}
		})
	}
}

func TestJournalCommissionClarificationAtomicDedupAndNoReplay(t *testing.T) {
	j := commissionJournal(t)
	data := CommissionClarificationJob{Version: 1, OrderID: "901", OrderVersion: 4, BuyerID: commissionTestAgent, RequestID: strings.Repeat("a", 64), Round: 1}
	queue := func(version int64) {
		data.OrderVersion = version
		mustAddCommission(t, j, commissionNotification(901, version, "buyer", true))
		source, ok, err := j.NextKind("commission_order")
		if err != nil || !ok {
			t.Fatalf("source %v %v", ok, err)
		}
		if err := j.QueueCommissionClarification(source.ID, data); err != nil {
			t.Fatal(err)
		}
	}
	queue(4)
	if _, ok, err := j.Next(); err != nil || ok {
		t.Fatalf("general lane claimed clarification: %v %v", ok, err)
	}
	job, ok, err := j.NextKind("commission_clarification")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	// A later notification of the same authoritative question cannot repeat it.
	queue(5)
	jobs := j.Snapshot()
	if len(jobs) != 3 {
		t.Fatalf("duplicate clarification: %d", len(jobs))
	}
	for _, status := range jobs {
		if status.Data != nil {
			t.Fatal("status leaked question payload")
		}
	}
	// Interrupted Agent admission stays unknown rather than resuming automatically.
	j, err = OpenJournal(j.binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err = j.NextKind("commission_clarification"); err != nil || ok {
		t.Fatalf("uncertain answer replayed: %v %v", ok, err)
	}
	for _, saved := range j.Snapshot() {
		if saved.ID == job.ID && saved.Status != "unknown" {
			t.Fatal("lost uncertainty")
		}
	}
}

func TestJournalCommissionClarificationRejectsWrongRoleAndRound(t *testing.T) {
	for _, scenario := range []string{"seller", "wrong_buyer", "too_many_rounds"} {
		t.Run(scenario, func(t *testing.T) {
			j := commissionJournal(t)
			role := "buyer"
			if scenario == "seller" {
				role = "seller"
			}
			mustAddCommission(t, j, commissionNotification(901, 4, role, true))
			source, _, _ := j.NextKind("commission_order")
			data := CommissionClarificationJob{Version: 1, OrderID: "901", OrderVersion: 4, BuyerID: commissionTestAgent, RequestID: strings.Repeat("a", 64), Round: 1}
			if scenario == "wrong_buyer" {
				data.BuyerID = "43"
			}
			if scenario == "too_many_rounds" {
				data.Round = 4
			}
			if j.QueueCommissionClarification(source.ID, data) == nil {
				t.Fatal("unsafe queue admitted")
			}
			if len(j.Snapshot()) != 1 || j.Snapshot()[0].Status != "running" {
				t.Fatal("failed queue modified source")
			}
		})
	}
}
