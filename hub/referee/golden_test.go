package referee

import (
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"fluxspace.ai/hub/models"
)

type rawAction struct {
	ID       string  `json:"id"`
	Family   string  `json:"family"`
	Risk     string  `json:"risk"`
	InfoGain float64 `json:"infoGain"`
	TeamHarm float64 `json:"teamHarm"`
	Contest  float64 `json:"contest"`
	Effects  struct {
		Metrics map[string]float64 `json:"metrics"`
	} `json:"effects"`
}

func (r rawAction) model() models.Action {
	return models.Action{
		ID: r.ID, Family: r.Family, Risk: r.Risk,
		InfoGain: r.InfoGain, TeamHarm: r.TeamHarm, Contest: r.Contest,
		Effects: r.Effects.Metrics,
	}
}

type rawMetric struct {
	Value float64 `json:"value"`
	Max   float64 `json:"max"`
	Good  string  `json:"good"`
}

type refereeGolden struct {
	Fatigue []struct {
		In  int     `json:"in"`
		Out float64 `json:"out"`
	} `json:"fatigue"`
	RoundDelta []struct {
		Name string `json:"name"`
		In   struct {
			Entries []struct {
				ActorID  string    `json:"actorId"`
				IsPlayer bool      `json:"isPlayer"`
				Side     string    `json:"side"`
				Action   rawAction `json:"action"`
			} `json:"entries"`
			Metrics    map[string]rawMetric `json:"metrics"`
			PlayerSide string               `json:"playerSide"`
			Mode       string               `json:"mode"`
			Usage      map[string]int       `json:"usage"`
			Rounds     int                  `json:"rounds"`
		} `json:"in"`
		Out struct {
			Delta        map[string]float64 `json:"delta"`
			Contribution map[string]float64 `json:"contribution"`
			Agreement    float64            `json:"agreement"`
			Synergy      float64            `json:"synergy"`
		} `json:"out"`
	} `json:"roundDelta"`
	Score []struct {
		Name string `json:"name"`
		In   struct {
			Action rawAction `json:"action"`
			Ctx    struct {
				Companion struct {
					Vector struct {
						Risk       float64 `json:"risk"`
						Logic      float64 `json:"logic"`
						Empathy    float64 `json:"empathy"`
						Coop       float64 `json:"coop"`
						Aggression float64 `json:"aggression"`
						Intuition  float64 `json:"intuition"`
					} `json:"vector"`
				} `json:"companion"`
				Mode             string  `json:"mode"`
				RoundIndex       int     `json:"roundIndex"`
				EvidenceCount    int     `json:"evidenceCount"`
				TeamIntent       *string `json:"teamIntent"`
				TeamFamily       string  `json:"teamFamily"`
				PlayerLastFamily string  `json:"playerLastFamily"`
				Affinity         float64 `json:"affinity"`
				Mood             float64 `json:"mood"`
			} `json:"ctx"`
		} `json:"in"`
		Out struct {
			Total     float64            `json:"total"`
			Breakdown map[string]float64 `json:"breakdown"`
		} `json:"out"`
	} `json:"score"`
	Condition []struct {
		Name string `json:"name"`
		In   struct {
			Cond    map[string]any       `json:"cond"`
			Metrics map[string]rawMetric `json:"metrics"`
		} `json:"in"`
		Out bool `json:"out"`
	} `json:"condition"`
}

func loadRefereeGolden(t *testing.T) refereeGolden {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "golden.json"))
	if err != nil {
		t.Fatalf("golden.json missing (run node hub/testdata/export_golden.js): %v", err)
	}
	var g refereeGolden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatalf("bad golden.json: %v", err)
	}
	return g
}

func metricsFromRaw(raw map[string]rawMetric) map[string]models.Metric {
	out := make(map[string]models.Metric, len(raw))
	for k, v := range raw {
		out[k] = models.Metric{Key: k, Value: v.Value, Max: v.Max, Good: v.Good}
	}
	return out
}

func parseCondition(raw map[string]any) models.Condition {
	c := models.Condition{}
	if v, ok := raw["metric"].(string); ok {
		c.Metric = v
	}
	if v, ok := raw["op"].(string); ok {
		c.Op = v
	}
	if v, ok := raw["value"]; ok {
		c.Value = v
	}
	if list, ok := raw["all"].([]any); ok {
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				c.All = append(c.All, parseCondition(m))
			}
		}
	}
	if list, ok := raw["any"].([]any); ok {
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				c.Any = append(c.Any, parseCondition(m))
			}
		}
	}
	return c
}

func closeEnough(got, want float64) bool {
	return math.Abs(got-want) <= 1e-9
}

func checkFloatMap(t *testing.T, label string, got, want map[string]float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: got %d keys %v, want %d keys %v", label, len(got), got, len(want), want)
		return
	}
	for k, wantV := range want {
		gotV, ok := got[k]
		if !ok {
			t.Errorf("%s: missing key %q", label, k)
			continue
		}
		if !closeEnough(gotV, wantV) {
			t.Errorf("%s[%q] = %.12g, prototype says %.12g", label, k, gotV, wantV)
		}
	}
}

func TestFatigueMulMatchesPrototype(t *testing.T) {
	g := loadRefereeGolden(t)
	for _, c := range g.Fatigue {
		if got := FatigueMul(c.In); !closeEnough(got, c.Out) {
			t.Errorf("FatigueMul(%d) = %v, prototype says %v", c.In, got, c.Out)
		}
	}
}

func TestComputeRoundDeltaMatchesPrototype(t *testing.T) {
	g := loadRefereeGolden(t)
	if len(g.RoundDelta) == 0 {
		t.Fatal("no round-delta cases")
	}
	for _, c := range g.RoundDelta {
		entries := make([]RoundEntry, 0, len(c.In.Entries))
		for _, e := range c.In.Entries {
			entries = append(entries, RoundEntry{
				ActorID: e.ActorID, IsPlayer: e.IsPlayer, Side: e.Side, Action: e.Action.model(),
			})
		}
		got := ComputeRoundDelta(entries, metricsFromRaw(c.In.Metrics), c.In.PlayerSide, c.In.Mode, c.In.Usage, c.In.Rounds)
		checkFloatMap(t, c.Name+".delta", got.Delta, c.Out.Delta)
		checkFloatMap(t, c.Name+".contribution", got.Contribution, c.Out.Contribution)
		if !closeEnough(got.Agreement, c.Out.Agreement) {
			t.Errorf("%s agreement = %v, want %v", c.Name, got.Agreement, c.Out.Agreement)
		}
		if !closeEnough(got.Synergy, c.Out.Synergy) {
			t.Errorf("%s synergy = %v, want %v", c.Name, got.Synergy, c.Out.Synergy)
		}
	}
}

func TestScoreActionMatchesPrototype(t *testing.T) {
	g := loadRefereeGolden(t)
	if len(g.Score) == 0 {
		t.Fatal("no score cases")
	}
	for _, c := range g.Score {
		v := c.In.Ctx.Companion.Vector
		ctx := DecisionContext{
			Personality: models.Personality{
				Risk: v.Risk, Logic: v.Logic, Empathy: v.Empathy,
				Coop: v.Coop, Aggression: v.Aggression, Intuition: v.Intuition,
			},
			Mode: c.In.Ctx.Mode, RoundIndex: c.In.Ctx.RoundIndex,
			EvidenceCount: c.In.Ctx.EvidenceCount,
			TeamFamily:    c.In.Ctx.TeamFamily, PlayerLastFamily: c.In.Ctx.PlayerLastFamily,
			Affinity: c.In.Ctx.Affinity, Mood: c.In.Ctx.Mood,
			// RNG nil => noise 0, matching Math.random frozen at 0.5.
		}
		if c.In.Ctx.TeamIntent != nil {
			ctx.TeamIntent = *c.In.Ctx.TeamIntent
		}
		got := ScoreAction(c.In.Action.model(), ctx)
		if !closeEnough(got.Total, c.Out.Total) {
			t.Errorf("%s total = %.12g, prototype says %.12g", c.Name, got.Total, c.Out.Total)
		}
		checkFloatMap(t, c.Name+".breakdown", got.Breakdown, c.Out.Breakdown)
	}
}

func TestEvalConditionMatchesPrototype(t *testing.T) {
	g := loadRefereeGolden(t)
	for _, c := range g.Condition {
		got := EvalCondition(parseCondition(c.In.Cond), metricsFromRaw(c.In.Metrics))
		if got != c.Out {
			t.Errorf("%s = %v, prototype says %v", c.Name, got, c.Out)
		}
	}
}

func TestCheckEndNeverReturnsNeutralTimeout(t *testing.T) {
	win := models.Condition{All: []models.Condition{
		{Metric: "truth", Op: ">=", Value: 80},
		{Metric: "suspicion", Op: "<", Value: 70},
	}}
	lose := models.Condition{Any: []models.Condition{
		{Metric: "suspicion", Op: ">=", Value: 92},
		{Metric: "time", Op: "<=", Value: 0},
	}}
	cases := []struct {
		name  string
		m     map[string]models.Metric
		round int
		want  Outcome
	}{
		{"win", map[string]models.Metric{"truth": {Value: 85}, "suspicion": {Value: 40}}, 3, OutcomeWin},
		{"lose", map[string]models.Metric{"truth": {Value: 20}, "suspicion": {Value: 95}}, 3, OutcomeLose},
		{"partial", map[string]models.Metric{"truth": {Value: 70, Max: 100}, "suspicion": {Value: 40, Max: 100}}, 5, OutcomePartial},
		{"regret", map[string]models.Metric{"truth": {Value: 30, Max: 100}, "suspicion": {Value: 80, Max: 100}}, 5, OutcomeRegret},
		{"continue", map[string]models.Metric{"truth": {Value: 30}, "suspicion": {Value: 40}}, 2, ""},
	}
	for _, c := range cases {
		if got := CheckEnd(win, lose, c.m, c.round, 5); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDecideActionIsDeterministic(t *testing.T) {
	actions := []models.Action{
		{ID: "a", Family: "verify", Risk: "low", InfoGain: 3, TeamHarm: 1, Effects: map[string]float64{"truth": 14}},
		{ID: "b", Family: "probe", Risk: "high", InfoGain: 1, TeamHarm: -1, Contest: 1, Effects: map[string]float64{"truth": 6}},
	}
	ctx := DecisionContext{
		Personality: models.Personality{Risk: 20, Logic: 80, Empathy: 60, Coop: 70, Aggression: 10, Intuition: 90},
		Actions:     actions, Mode: "coop", RoundIndex: 0, EvidenceCount: 3, Mood: 50, Affinity: 30,
		RNG: rand.New(rand.NewSource(42)),
	}
	first, err := DecideAction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ctx.RNG = rand.New(rand.NewSource(42))
	second, err := DecideAction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.Action.ID != second.Action.ID || first.Confidence != second.Confidence {
		t.Error("same seed produced different decisions")
	}
	if _, err := DecideAction(DecisionContext{}); err == nil {
		t.Error("empty action pool must fail, not silently degrade")
	}
}
