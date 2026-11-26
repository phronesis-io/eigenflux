package referee

import "fluxspace.ai/hub/models"

// Outcome is a match ending. An empty Outcome means "keep playing".
type Outcome string

const (
	OutcomeWin       Outcome = "win"
	OutcomeLose      Outcome = "lose"
	OutcomePartial   Outcome = "partial"
	OutcomeRegret    Outcome = "regret"
	OutcomeRoundsOut Outcome = "rounds_out"
)

// EvalCondition mirrors evalCondition() in the prototype. Recursive all/any
// groups are evaluated before a leaf comparison.
func EvalCondition(c models.Condition, metrics map[string]models.Metric) bool {
	if len(c.All) > 0 {
		for _, sub := range c.All {
			if !EvalCondition(sub, metrics) {
				return false
			}
		}
		return true
	}
	if len(c.Any) > 0 {
		for _, sub := range c.Any {
			if EvalCondition(sub, metrics) {
				return true
			}
		}
		return false
	}
	m, ok := metrics[c.Metric]
	if !ok {
		return false
	}
	rhs, ok := resolveValue(c.Value, metrics)
	if !ok {
		return false
	}
	return compare(m.Value, c.Op, rhs)
}

func resolveValue(v any, metrics map[string]models.Metric) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		// The JS original returns 0 for a missing sentinel metric.
		if x == "__their__" {
			m, ok := metrics["their"]
			if !ok {
				return 0, true
			}
			return m.Value, true
		}
		if x == "__my__" {
			m, ok := metrics["my"]
			if !ok {
				return 0, true
			}
			return m.Value, true
		}
		return 0, false
	default:
		return 0, false
	}
}

func compare(a float64, op string, b float64) bool {
	switch op {
	case ">=":
		return a >= b
	case ">":
		return a > b
	case "<=":
		return a <= b
	case "<":
		return a < b
	case "==":
		return a == b
	case "!=":
		return a != b
	default:
		return false
	}
}

// CheckEnd applies win, then lose, then the round limit. A round-limit finish
// is never a neutral timeout: progress decides partial success or regret.
func CheckEnd(win, lose models.Condition, metrics map[string]models.Metric, roundIndex, totalRounds int) Outcome {
	if EvalCondition(win, metrics) {
		return OutcomeWin
	}
	if EvalCondition(lose, metrics) {
		return OutcomeLose
	}
	if roundIndex >= totalRounds {
		return roundsOutOutcome(metrics)
	}
	return ""
}

func roundsOutOutcome(metrics map[string]models.Metric) Outcome {
	t, okT := metrics["truth"]
	s, okS := metrics["suspicion"]
	if !okT || !okS || t.Max <= 0 || s.Max <= 0 {
		return OutcomeRegret
	}
	progress := t.Value / t.Max
	danger := s.Value / s.Max
	if progress >= 0.6 && danger < 0.7 {
		return OutcomePartial
	}
	return OutcomeRegret
}
