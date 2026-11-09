package maintenance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRuleGateSurvivesLegacyCLIStateRewrite(t *testing.T) {
	s := Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	c, err := StartCycle(s, "full", []string{"feed"}, RuntimeObservation{})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "rules.md")
	if err := os.WriteFile(p, []byte("visible rule body"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SetCycleRules(s, c.ID, []string{p}); err != nil {
		t.Fatal(err)
	}
	r, _, err := ReadCycleRule(s, c.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := AcknowledgeCycleRule(s, c.ID, 1, r.Digest); err != nil {
		t.Fatal(err)
	}
	if err := BindStopCycle(s, "owner", c.ID); err != nil {
		t.Fatal(err)
	}
	path, _ := s.path()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	// The running pre-upgrade watch knows only these Cycle fields.
	var oldCycles map[string]struct {
		ID                string            `json:"id"`
		StartedAt         int64             `json:"started_at"`
		Mode              string            `json:"mode"`
		Stages            map[string]string `json:"stages"`
		Result            string            `json:"result,omitempty"`
		ExecutionObserved bool              `json:"execution_observed,omitempty"`
	}
	if err := json.Unmarshal(state["cycles"], &oldCycles); err != nil {
		t.Fatal(err)
	}
	state["cycles"], err = json.Marshal(oldCycles)
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	after, err := ReadCycle(s, c.ID)
	if err != nil || len(after.Rules) != 1 || !after.Rules[0].Acknowledged {
		t.Fatalf("legacy writer erased rules: %+v %v", after, err)
	}
	if id, err := StopCycle(s, "owner"); err != nil || id != c.ID {
		t.Fatal("legacy writer erased continuation binding")
	}
	if err := ReportCycle(s, c.ID, "feed", "completed"); err != nil {
		t.Fatal(err)
	}
	if err := ReportCycle(s, c.ID, "cycle", "completed"); err != nil {
		t.Fatal(err)
	}
}

func TestCycleRulesVisibleReceiptAndCompletionGate(t *testing.T) {
	s := Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	c, err := StartCycle(s, "full", []string{"feed", "publish"}, RuntimeObservation{})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "rules.md")
	body := strings.Repeat("规则正文\n", 900)
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SetCycleRules(s, c.ID, []string{p}); err != nil {
		t.Fatal(err)
	}
	if ReportCycle(s, c.ID, "feed", "completed") == nil {
		t.Fatal("unread rules accepted")
	}
	c, _ = ReadCycle(s, c.ID)
	joined := ""
	for i := range c.Rules {
		r, b, err := ReadCycleRule(s, c.ID, i+1)
		if err != nil || len(b) > RulePageBytes || !utf8.Valid(b) {
			t.Fatalf("bad page: %v", err)
		}
		joined += string(b)
		if AcknowledgeCycleRule(s, c.ID, i+1, "wrong") == nil {
			t.Fatal("invented receipt accepted")
		}
		if err := AcknowledgeCycleRule(s, c.ID, i+1, r.Digest); err != nil {
			t.Fatal(err)
		}
	}
	if joined != body {
		t.Fatal("rule body lost")
	}
	if err := ReportCycle(s, c.ID, "feed", "completed"); err != nil {
		t.Fatal(err)
	}
	c, _ = ReadCycle(s, c.ID)
	rules, stages := CycleMissing(c)
	if len(rules) != 0 || len(stages) != 1 || stages[0] != "publish" {
		t.Fatalf("missed Publish omission: %v %v", rules, stages)
	}
	if ReportCycle(s, c.ID, "cycle", "completed") == nil {
		t.Fatal("missing stage completed")
	}
	if err := ReportCycle(s, c.ID, "publish", "completed"); err != nil {
		t.Fatal(err)
	}
	if err := ReportCycle(s, c.ID, "cycle", "completed"); err != nil {
		t.Fatal(err)
	}
}

func TestCycleRuleChangeAndCrossCycleReceiptRejected(t *testing.T) {
	s := Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	p := filepath.Join(t.TempDir(), "rules.md")
	os.WriteFile(p, []byte("original"), 0600)
	a, _ := StartCycle(s, "full", []string{"feed"}, RuntimeObservation{})
	b, _ := StartCycle(s, "full", []string{"feed"}, RuntimeObservation{})
	SetCycleRules(s, a.ID, []string{p})
	SetCycleRules(s, b.ID, []string{p})
	r, _, _ := ReadCycleRule(s, a.ID, 1)
	if AcknowledgeCycleRule(s, b.ID, 1, r.Digest) == nil {
		t.Fatal("cross-cycle receipt accepted")
	}
	os.WriteFile(p, []byte("modified"), 0600)
	if _, _, err := ReadCycleRule(s, a.ID, 1); err == nil {
		t.Fatal("changed source accepted")
	}
}
