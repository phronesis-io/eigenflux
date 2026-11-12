package services

import (
	"strings"
	"testing"

	"fluxspace.ai/hub/models"
	"fluxspace.ai/hub/referee"
)

func testContext() referee.DecisionContext {
	return referee.DecisionContext{
		Personality: models.Personality{
			Risk: 30, Logic: 80, Empathy: 60, Coop: 70, Aggression: 10, Intuition: 90,
		},
		Actions: []models.Action{
			{ID: "a_evidence", Label: "Trace the evidence chain", Family: "evidence", Risk: "mid",
				InfoGain: 3, TeamHarm: 1, Effects: map[string]float64{"truth": 14}},
			{ID: "a_wait", Label: "Observe in silence", Family: "wait", Risk: "low",
				InfoGain: 0, TeamHarm: -1, Effects: map[string]float64{"truth": 2}},
		},
		Mode: "coop", RoundIndex: 1, EvidenceCount: 3, Mood: 50, Affinity: 30,
	}
}

func TestRuleEngineDecideHasNoNetworkDependency(t *testing.T) {
	a := NewRuleEngineAdapter()
	d, err := a.Decide(testContext())
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != "rule_engine" {
		t.Errorf("source = %q, want rule_engine", d.Source)
	}
	if d.ActionID == "" || d.ReasoningSummary == "" {
		t.Error("decision is missing action id or summary")
	}
	if d.Confidence < 20 || d.Confidence > 97 {
		t.Errorf("confidence %.1f outside GDD clamp [20,97]", d.Confidence)
	}
}

func TestRuleEngineDecideRejectsEmptyPool(t *testing.T) {
	a := NewRuleEngineAdapter()
	if _, err := a.Decide(referee.DecisionContext{}); err == nil {
		t.Error("empty action pool must fail loudly, not fall back silently")
	}
}

func TestRuleEngineDiscussRespectsCharacterCap(t *testing.T) {
	a := NewRuleEngineAdapter()
	line, err := a.Discuss(testContext())
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(line)) > 200 || line == "" {
		t.Errorf("discussion line length %d is invalid", len([]rune(line)))
	}
}

func TestRuleEngineReflectIsBoundedAndSourceTagged(t *testing.T) {
	a := NewRuleEngineAdapter()
	events := []string{"first", "second", "third", "fourth", strings.Repeat("x", 900)}
	mem, err := a.Reflect("comp_1", events)
	if err != nil {
		t.Fatal(err)
	}
	if len(mem) == 0 || len(mem) > 3 {
		t.Errorf("reflection produced %d memories, want 1-3", len(mem))
	}
	total := 0
	for _, m := range mem {
		if m.Kind != models.MemoryPersonalReflection {
			t.Errorf("memory kind = %q", m.Kind)
		}
		if m.CompanionID != "comp_1" || m.Source.Kind == "" || m.Source.RefID == "" {
			t.Errorf("memory is not source-tagged: %+v", m.Source)
		}
		total += len([]rune(m.Content))
	}
	if total > 500 {
		t.Errorf("reflection used %d characters, over the 500-token ceiling approximation", total)
	}
	if _, err := a.Reflect("", events); err == nil {
		t.Error("empty companion id must be rejected")
	}
	if empty, err := a.Reflect("comp_1", nil); err != nil || len(empty) != 0 {
		t.Errorf("no events should yield no memories, got %d (%v)", len(empty), err)
	}
}
