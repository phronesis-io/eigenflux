package generator

import (
	"math/rand"
	"testing"

	"fluxspace.ai/hub/referee"
)

func calibrationFixture(t *testing.T) *Calibration {
	t.Helper()
	s, err := GenerateSkeleton(GenerateOptions{Mode: "coop", CompanionCount: 3}, rand.New(rand.NewSource(1)))
	if err != nil {
		t.Fatal(err)
	}
	c := Calibrate(s, 160, 4, rand.New(rand.NewSource(99)))
	return &c
}

func TestCalibrationRuns480Matches(t *testing.T) {
	c := calibrationFixture(t)
	t.Logf("optimal %.1f%% (%d/160) | random %.1f%% (%d/160) | worst %.1f%% (%d/160) | non-rounds-out %.1f%% | validated=%v",
		c.OptimalWinRate*100, c.OptimalWins,
		c.RandomWinRate*100, c.RandomWins,
		c.WorstWinRate*100, c.WorstWins,
		c.NonRoundsOutRate*100, c.Validated)
	if c.TotalMatches != 480 {
		t.Errorf("expected 480 matches, got %d", c.TotalMatches)
	}
	if c.OptimalWinRate < c.WorstWinRate {
		t.Errorf("optimal path (%.2f) must not be worse than worst path (%.2f)", c.OptimalWinRate, c.WorstWinRate)
	}
	if c.RandomWinRate > c.OptimalWinRate || c.RandomWinRate < c.WorstWinRate {
		t.Errorf("random path (%.2f) must sit between worst (%.2f) and optimal (%.2f)",
			c.RandomWinRate, c.WorstWinRate, c.OptimalWinRate)
	}
}

// ADR-0003 records that the GDD three-path win-rate targets (~90% / 40-60% /
// ~10%) and the ">=60% non-rounds-out" target are retired for the MVP by owner
// design decision. Meeting those retired targets is no longer an error. This
// test now records both simulators' measured values and asserts the fact that
// they are different measurements, not two results of one measurement:
//
//   - Authoritative G1 baseline: `cd fluxspace && node _balance.js` (full
//     Runtime) — 480 matches, 7 wins / 41 losses / 432 timeouts = 10%
//     non-rounds-out. It measures random/greedy win-lose-timeout and does NOT
//     measure three-path win rates.
//   - Go structural self-check (this package): one fixed skeleton replayed
//     480 times with a simplified three-strategy seat picker and no branch
//     events — three paths all 0%, 0% non-rounds-out; used only for skeleton
//     reachability and path ordering, not balance acceptance.
//
// Figures are recorded as measured. Thresholds, settlement maths and the
// authoritative baseline are unchanged; no new percentage is claimed as a
// measured target. See docs/architecture/adr-0003-relaxed-calibration-targets.md.
func TestCalibrationGDDGapIsTracked(t *testing.T) {
	const (
		prototypeWins     = 7
		prototypeLosses   = 41
		prototypeTimeouts = 432
		prototypeMatches  = 480
	)
	prototypeNonRoundsOut := float64(prototypeWins+prototypeLosses) / prototypeMatches

	c := calibrationFixture(t)
	goNonRoundsOut := c.NonRoundsOutRate

	t.Logf("ADR-0003 recorded measurement: authoritative _balance.js = %.1f%% non-rounds-out "+
		"(%d wins / %d losses / %d timeouts over %d matches, random+greedy win-lose-timeout); "+
		"Go structural self-check = optimal %.1f%% / random %.1f%% / worst %.1f%% / non-rounds-out %.1f%%",
		prototypeNonRoundsOut*100, prototypeWins, prototypeLosses, prototypeTimeouts, prototypeMatches,
		c.OptimalWinRate*100, c.RandomWinRate*100, c.WorstWinRate*100, goNonRoundsOut*100)

	if prototypeWins+prototypeLosses+prototypeTimeouts != prototypeMatches {
		t.Fatalf("prototype baseline counts do not sum to %d", prototypeMatches)
	}
	if goNonRoundsOut == prototypeNonRoundsOut {
		t.Errorf("the two simulators must remain different measurements: Go structural self-check "+
			"non-rounds-out %.1f%% unexpectedly equals the authoritative _balance.js baseline %.1f%%; "+
			"if the Go port became full-Runtime-equivalent, re-derive the authoritative baseline rather "+
			"than substituting this value (ADR-0003 'Not claimed done')",
			goNonRoundsOut*100, prototypeNonRoundsOut*100)
	}
}

// Story 004 AC9/AC10: running out of rounds must resolve to partial success or
// regret, never a neutral timeout.
func TestSimulateMatchRoundLimitIsNeverNeutral(t *testing.T) {
	s, err := GenerateSkeleton(GenerateOptions{Mode: "coop", CompanionCount: 3}, rand.New(rand.NewSource(3)))
	if err != nil {
		t.Fatal(err)
	}
	for _, strategy := range []Strategy{StrategyOptimal, StrategyRandom, StrategyWorst} {
		r := SimulateMatch(s, strategy, 4, rand.New(rand.NewSource(5)))
		if r.Outcome != referee.OutcomePartial && r.Outcome != referee.OutcomeRegret &&
			r.Outcome != referee.OutcomeWin && r.Outcome != referee.OutcomeLose {
			t.Errorf("%s produced neutral outcome %q", strategy, r.Outcome)
		}
		if r.EndRound < 1 || r.EndRound > len(s.Rounds) {
			t.Errorf("%s ended on invalid round %d", strategy, r.EndRound)
		}
	}
}
