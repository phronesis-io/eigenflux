// Package referee implements the deterministic match runtime: round settlement,
// personality-weighted decision scoring, condition evaluation and endings.
// The maths is transcribed from fluxspace/_check.js, which is frozen as the
// executable specification. Do not "simplify" the constants without re-running
// the golden and 480-match balance tests.
package referee

import (
	"fmt"
	"math"
	"math/rand"
	"sort"

	"fluxspace.ai/hub/models"
)

// RoundEntry is one submitted decision in a round.
type RoundEntry struct {
	ActorID  string
	IsPlayer bool
	Side     string
	Action   models.Action
}

// RoundDelta is the settled metric change for one round.
type RoundDelta struct {
	Delta        map[string]float64
	Contribution map[string]float64
	Agreement    float64
	Synergy      float64
}

// FatigueMul is the marginal-decay valve: repeating one action family beyond
// its first use loses 28% effectiveness per repeat, floored at 0.42.
func FatigueMul(timesUsed int) float64 {
	if timesUsed <= 1 {
		return 1
	}
	return math.Max(0.42, 1-float64(timesUsed-1)*0.28)
}

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}

// ComputeRoundDelta mirrors computeRoundDelta() in the prototype. Effects are
// accumulated as a weighted average (not a sum) so a 4-seat match does not
// finish four times faster than a solo match, then scaled by synergy and pace.
func ComputeRoundDelta(entries []RoundEntry, metrics map[string]models.Metric, playerSide, mode string, usage map[string]int, roundCount int) RoundDelta {
	const wPlayer, wAI = 1.6, 1.0
	acc := make(map[string]float64)
	contribution := make(map[string]float64)
	fam := make(map[string]int)
	var wSum float64

	for _, e := range entries {
		w := wAI
		if e.IsPlayer {
			w = wPlayer
		}
		wSum += w
		family := e.Action.Family
		if family == "" {
			family = "misc"
		}
		fam[family]++
		fatigue := FatigueMul(usage[family])
		var gain float64

		for k, v := range e.Action.Effects {
			m, ok := metrics[k]
			if !ok {
				continue
			}
			if mode == "versus" && e.Side != playerSide {
				switch k {
				case "my":
					v = -math.Abs(v) * 0.7
				case "their":
					v = math.Abs(v) * 0.7
				case "momentum":
					v = -math.Abs(v) * 0.6
				default:
					v = -v * 0.55
				}
			}
			scaled := v * fatigue
			acc[k] += scaled * w
			positive := scaled
			if m.Good == "low" {
				positive = -scaled
			}
			if e.Side == playerSide {
				gain += math.Max(0, positive) * w
			}
		}
		if e.Side == playerSide {
			contribution[e.ActorID] += math.Round(gain)
		}
	}

	maxFam := 1
	for _, count := range fam {
		if count > maxFam {
			maxFam = count
		}
	}
	agreement := 0.0
	if len(entries) > 0 {
		agreement = float64(maxFam) / float64(len(entries))
	}
	synergy := 1 + 0.12*agreement
	paceScale := 1.0
	if roundCount >= 6 {
		paceScale = 0.82
	} else if roundCount <= 3 {
		paceScale = 1.6
	}

	delta := make(map[string]float64, len(acc))
	for k, v := range acc {
		delta[k] = (v / math.Max(1, wSum)) * synergy * paceScale
	}
	return RoundDelta{Delta: delta, Contribution: contribution, Agreement: agreement, Synergy: synergy}
}

// DecisionContext is everything the scoring function needs. RNG is injected so
// the local rule engine is fully reproducible in tests; nil means no noise.
type DecisionContext struct {
	Personality      models.Personality
	Actions          []models.Action
	Mode             string
	RoundIndex       int
	EvidenceCount    int
	TeamIntent       string
	TeamFamily       string
	PlayerLastFamily string
	Affinity         float64
	Mood             float64
	RNG              *rand.Rand
}

// ScoredAction is one candidate with its breakdown, used by the three-density
// decision summaries in the spectator client.
type ScoredAction struct {
	Action    models.Action
	Total     float64
	Breakdown map[string]float64
}

var riskSign = map[string]float64{"low": -1, "mid": 0, "high": 1}

func norm(v float64) float64 { return (v - 50) / 50 }

// ScoreAction mirrors scoreAction() in the prototype. An empty TeamIntent is
// treated the same way the JS original treats null (no team signal).
func ScoreAction(a models.Action, ctx DecisionContext) ScoredAction {
	c := ctx.Personality
	b := make(map[string]float64, 11)
	b["info"] = 1.35 * a.InfoGain
	b["team"] = 0.95 * a.TeamHarm
	b["risk"] = 1.90 * norm(c.Risk) * riskSign[a.Risk]

	support := clamp(float64(ctx.EvidenceCount)/float64(ctx.RoundIndex+2), 0, 1)
	logicFactor := -0.4
	if a.InfoGain > 0 {
		logicFactor = support
	}
	b["logic"] = 0.80 * norm(c.Logic) * logicFactor
	b["empathy"] = -1.50 * norm(c.Empathy) * math.Max(0, -a.TeamHarm)

	agreement := 0.0
	if ctx.TeamIntent != "" {
		switch {
		case a.ID == ctx.TeamIntent:
			agreement = 1
		case a.Family != "" && a.Family == ctx.TeamFamily:
			agreement = 0.3
		default:
			agreement = -0.2
		}
	}
	b["coop"] = 1.60 * norm(c.Coop) * agreement

	modeWeight := 0.15
	if ctx.Mode == "versus" {
		modeWeight = 1
	}
	b["aggr"] = 1.40 * norm(c.Aggression) * a.Contest * modeWeight

	intuitionFactor := 0.25
	if a.InfoGain < 1 {
		intuitionFactor = 1
	}
	b["intuition"] = 1.20 * norm(c.Intuition) * intuitionFactor

	if ctx.PlayerLastFamily != "" && a.Family == ctx.PlayerLastFamily {
		b["affinity"] = ((ctx.Affinity - 30) / 100) * 0.9
	} else {
		b["affinity"] = 0
	}
	b["mood"] = ((50 - ctx.Mood) / 50) * norm(c.Risk) * riskSign[a.Risk] * 1.1

	noise := 0.0
	if ctx.RNG != nil {
		noise = (ctx.RNG.Float64() - 0.5) * (1.2 - (c.Intuition/100)*0.9) * 1.6
	}
	b["noise"] = noise

	total := 0.0
	for _, v := range b {
		total += v
	}
	return ScoredAction{Action: a, Total: total, Breakdown: b}
}

// DecisionResult is the deterministic outcome of one decision call.
type DecisionResult struct {
	Action     models.Action
	Confidence float64
	Margin     float64
	Scores     []ScoredAction
}

// DecideAction picks the highest-scoring legal action and derives confidence
// from the margin over the runner-up. Ties keep pool order (stable sort).
func DecideAction(ctx DecisionContext) (DecisionResult, error) {
	if len(ctx.Actions) == 0 {
		return DecisionResult{}, fmt.Errorf("empty action pool")
	}
	scored := make([]ScoredAction, 0, len(ctx.Actions))
	for _, a := range ctx.Actions {
		scored = append(scored, ScoreAction(a, ctx))
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Total > scored[j].Total })
	best := scored[0]
	margin := best.Total
	if len(scored) > 1 {
		margin = best.Total - scored[1].Total
	}
	confidence := clamp(math.Round(46+margin*13), 20, 97)
	return DecisionResult{Action: best.Action, Confidence: confidence, Margin: margin, Scores: scored}, nil
}
