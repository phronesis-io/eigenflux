package generator

import (
	"fmt"
	"math/rand"
	"sort"

	"fluxspace.ai/hub/models"
	"fluxspace.ai/hub/referee"
)

// Strategy is one of the three reachability paths from story 005 AC2.
type Strategy string

const (
	StrategyOptimal Strategy = "optimal"
	StrategyRandom  Strategy = "random"
	StrategyWorst   Strategy = "worst"
)

// MatchResult distinguishes an early decisive ending from a round-limit
// ending. CheckEnd maps round-limit finishes to partial/regret, so the
// EndedEarly flag is what story 005 AC10 actually measures.
type MatchResult struct {
	Outcome    referee.Outcome
	EndedEarly bool
	EndRound   int
}

// Calibration is the output of the three-path reachability simulation.
type Calibration struct {
	PerPath          int
	TotalMatches     int
	OptimalWins      int
	RandomWins       int
	WorstWins        int
	OptimalWinRate   float64
	RandomWinRate    float64
	WorstWinRate     float64
	NonRoundsOutRate float64
	// Validated is a legacy structural self-check flag. Per ADR-0003 it is
	// retained for the Go port's own skeleton reachability / path-ordering
	// sanity checks only; it is NOT an S2 balance-acceptance criterion. The
	// authoritative G1 baseline (fluxspace/_balance.js) does not measure
	// these three-path rates at all.
	Validated bool
}

func clampMetric(v, max float64) float64 {
	if max <= 0 {
		return v
	}
	if v < 0 {
		return 0
	}
	if v > max {
		return max
	}
	return v
}

// chooseAction spreads seats across the ranked action pool. The prototype's
// actors decide independently from their own personalities, so a real round
// contains a mix of action families. Picking the single best action for every
// seat would concentrate one family, trigger the fatigue valve immediately and
// produce a settlement curve the prototype never exhibits.
func chooseAction(actions []models.Action, strategy Strategy, seat int, rng *rand.Rand) models.Action {
	if len(actions) == 0 {
		return models.Action{}
	}
	value := func(a models.Action) float64 {
		return a.Effects["truth"] - a.Effects["suspicion"]*0.5
	}
	sorted := append([]models.Action(nil), actions...)
	sort.SliceStable(sorted, func(i, j int) bool { return value(sorted[i]) > value(sorted[j]) })
	offset := seat % len(sorted)
	switch strategy {
	case StrategyOptimal:
		return sorted[offset]
	case StrategyWorst:
		return sorted[len(sorted)-1-offset]
	default:
		return actions[rng.Intn(len(actions))]
	}
}

// SimulateMatch runs a full deterministic match using the Go port of the
// prototype's settlement maths. Branch events are deliberately excluded here:
// calibration measures the skeleton's solvability, not narrative variance.
func SimulateMatch(s *models.Skeleton, strategy Strategy, seats int, rng *rand.Rand) MatchResult {
	metrics := make(map[string]models.Metric, len(s.InitialMetrics))
	for _, m := range s.InitialMetrics {
		metrics[m.Key] = m
	}
	usage := make(map[string]int)
	totalRounds := len(s.Rounds)

	for i, round := range s.Rounds {
		entries := make([]referee.RoundEntry, 0, seats)
		for seat := 0; seat < seats; seat++ {
			entries = append(entries, referee.RoundEntry{
				ActorID:  fmt.Sprintf("seat_%d", seat),
				IsPlayer: seat == 0,
				Side:     "A",
				Action:   chooseAction(round.Actions, strategy, seat, rng),
			})
		}
		delta := referee.ComputeRoundDelta(entries, metrics, "A", "coop", usage, totalRounds)
		for k, d := range delta.Delta {
			m, ok := metrics[k]
			if !ok {
				continue
			}
			m.Value = clampMetric(m.Value+d, m.Max)
			metrics[k] = m
		}
		for k, tick := range s.RoundTick {
			m, ok := metrics[k]
			if !ok {
				continue
			}
			m.Value = clampMetric(m.Value+tick, m.Max)
			metrics[k] = m
		}
		for _, e := range entries {
			usage[e.Action.Family]++
		}
		outcome := referee.CheckEnd(s.WinCondition, s.LoseCondition, metrics, i+1, totalRounds)
		if outcome != "" {
			return MatchResult{
				Outcome:    outcome,
				EndedEarly: outcome == referee.OutcomeWin || outcome == referee.OutcomeLose,
				EndRound:   i + 1,
			}
		}
	}
	return MatchResult{Outcome: referee.OutcomePartial, EndedEarly: false, EndRound: totalRounds}
}

// Calibrate runs perPath matches on each of the three strategies.
// story 005 AC10 uses 480 matches total, i.e. perPath = 160.
func Calibrate(s *models.Skeleton, perPath, seats int, rng *rand.Rand) Calibration {
	c := Calibration{PerPath: perPath, TotalMatches: perPath * 3}
	endedEarly := 0
	for _, strategy := range []Strategy{StrategyOptimal, StrategyRandom, StrategyWorst} {
		wins := 0
		for i := 0; i < perPath; i++ {
			r := SimulateMatch(s, strategy, seats, rng)
			if r.Outcome == referee.OutcomeWin {
				wins++
			}
			if r.EndedEarly {
				endedEarly++
			}
		}
		rate := float64(wins) / float64(perPath)
		switch strategy {
		case StrategyOptimal:
			c.OptimalWins, c.OptimalWinRate = wins, rate
		case StrategyRandom:
			c.RandomWins, c.RandomWinRate = wins, rate
		case StrategyWorst:
			c.WorstWins, c.WorstWinRate = wins, rate
		}
	}
	c.NonRoundsOutRate = float64(endedEarly) / float64(c.TotalMatches)
	// Legacy structural self-check thresholds (ADR-0003). The numeric values
	// are deliberately left unchanged: they document the retired GDD targets
	// and keep the reachability / path-ordering invariants intact. They are no
	// longer an S2 balance-acceptance gate: the authoritative G1 baseline
	// (fluxspace/_balance.js) does not measure three-path win rates, and the
	// >=60% non-rounds-out target is retired in favour of a recorded 10%
	// regression floor. This flag is a structural self-check only.
	c.Validated = c.OptimalWinRate >= 0.80 &&
		c.RandomWinRate >= 0.30 && c.RandomWinRate <= 0.70 &&
		c.WorstWinRate <= 0.20 &&
		c.NonRoundsOutRate >= 0.60
	return c
}
