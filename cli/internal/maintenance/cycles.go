package maintenance

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

type Cycle struct {
	ID                string            `json:"id"`
	StartedAt         int64             `json:"started_at"`
	Mode              string            `json:"mode"`
	Stages            map[string]string `json:"stages"`
	Result            string            `json:"result,omitempty"`
	ExecutionObserved bool              `json:"execution_observed,omitempty"`
}

func cycleEvent(c Cycle, stage, result string) Event {
	e := NewEvent(c.ID, "runtime", "auto", "cycle", result)
	key := sha256.Sum256([]byte(c.ID + "\x00" + stage + "\x00" + result))
	e.EventID = hex.EncodeToString(key[:])
	var r RuntimeObservation
	r.Stage = stage
	r.PlanMode = c.Mode
	e.Runtime = &r
	return e
}
func StartCycle(scope Scope, mode string, stages []string, env RuntimeObservation) (Cycle, error) {
	c := Cycle{ID: NewID(), StartedAt: time.Now().UnixMilli(), Mode: mode, Stages: map[string]string{}}
	if len(stages) == 0 || len(stages) > 32 {
		return c, fmt.Errorf("cycle requires 1–32 stages")
	}
	for _, stage := range stages {
		if stage == "cycle" || stage == "" || !runtimeToken.MatchString(stage) {
			return c, fmt.Errorf("invalid stage")
		}
		c.Stages[stage] = ""
	}
	if err := Validate(cycleEvent(c, "cycle", "started")); err != nil {
		return c, err
	}
	e := NewEvent(c.ID, "runtime", "auto", "environment", "observed")
	e.Runtime = &env
	if err := Validate(e); err != nil {
		return c, err
	}
	err := update(scope, func(st *State) error {
		if st.Cycles == nil {
			st.Cycles = map[string]Cycle{}
		}
		if len(st.Cycles) >= 256 {
			oldestID := ""
			var oldestAt int64
			for id, previous := range st.Cycles {
				if previous.Result != "" && (oldestID == "" || previous.StartedAt < oldestAt) {
					oldestID, oldestAt = id, previous.StartedAt
				}
			}
			if oldestID == "" {
				return fmt.Errorf("cycle observation capacity reached")
			}
			delete(st.Cycles, oldestID)
		}
		st.Cycles[c.ID] = c
		st.Events = append(st.Events, e)
		return nil
	})
	return c, err
}

// Stage receipts are client assertions. A complete cycle requires every planned stage;
// neither plan creation nor an arbitrary terminal receipt can establish completion.
func ReportCycle(scope Scope, id, stage, result string) error {
	if !idValue.MatchString(id) || (result != "completed" && result != "failed") {
		return fmt.Errorf("invalid cycle receipt")
	}
	return update(scope, func(st *State) error {
		c, ok := st.Cycles[id]
		if !ok {
			return fmt.Errorf("unknown or expired cycle for this account")
		}
		if c.Result != "" {
			if (stage == "cycle" && result == c.Result) || (stage != "cycle" && c.Stages[stage] == result) {
				return nil
			}
			return fmt.Errorf("cycle already ended")
		}
		if stage == "cycle" {
			if result == "failed" {
				failed := false
				for _, outcome := range c.Stages {
					if outcome == "failed" {
						failed = true
						break
					}
				}
				if !failed {
					return fmt.Errorf("cycle failure requires a failed stage receipt")
				}
			}
			if result == "completed" {
				for _, v := range c.Stages {
					if v != "completed" {
						return fmt.Errorf("all planned stages must complete first")
					}
				}
			}
			c.Result = result
		} else {
			old, ok := c.Stages[stage]
			if !ok {
				return fmt.Errorf("stage not in this cycle")
			}
			if old == result {
				return nil
			}
			if old != "" {
				return fmt.Errorf("stage already reported; start a new cycle to retry")
			}
			c.Stages[stage] = result
		}
		e := cycleEvent(c, stage, result)
		if err := Validate(e); err != nil {
			return err
		}
		if !c.ExecutionObserved {
			start := cycleEvent(c, "cycle", "started")
			start.EventAt = e.EventAt // First execution evidence, not plan creation time.
			st.Events = append(st.Events, start)
			c.ExecutionObserved = true
		}
		st.Cycles[id] = c
		st.Events = append(st.Events, e)
		return nil
	})
}
