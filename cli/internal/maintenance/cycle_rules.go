package maintenance

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"unicode/utf8"
)

// Pages remain small enough to expose their body and receipt in one tool result.
const RulePageBytes = 6000

type CycleRule struct {
	Path         string `json:"path"`
	Start        int    `json:"start"`
	End          int    `json:"end"`
	Digest       string `json:"digest"`
	Acknowledged bool   `json:"acknowledged,omitempty"`
}

func ruleDigest(cycle string, page int, body []byte) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s:%d:", cycle, page)
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func SetCycleRules(scope Scope, id string, paths []string) error {
	var pages []CycleRule
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(body) == 0 || len(body) > 256<<10 || !utf8.Valid(body) {
			return fmt.Errorf("invalid rule source")
		}
		for start := 0; start < len(body); {
			end := min(start+RulePageBytes, len(body))
			for end < len(body) && !utf8.RuneStart(body[end]) {
				end--
			}
			pages = append(pages, CycleRule{Path: path, Start: start, End: end, Digest: ruleDigest(id, len(pages)+1, body[start:end])})
			start = end
		}
	}
	return update(scope, func(st *State) error {
		c, ok := st.Cycles[id]
		if !ok || c.Result != "" || len(c.Rules) > 0 {
			return fmt.Errorf("cycle rules already set or cycle unavailable")
		}
		c.Rules = pages
		st.Cycles[id] = c
		return nil
	})
}

func ReadCycle(scope Scope, id string) (Cycle, error) {
	st, err := Snapshot(scope)
	if err != nil {
		return Cycle{}, err
	}
	c, ok := st.Cycles[id]
	if !ok {
		return Cycle{}, fmt.Errorf("unknown or expired cycle for this account")
	}
	return c, nil
}

func ReadCycleRule(scope Scope, id string, page int) (CycleRule, []byte, error) {
	c, err := ReadCycle(scope, id)
	if err != nil {
		return CycleRule{}, nil, err
	}
	if page < 1 || page > len(c.Rules) {
		return CycleRule{}, nil, fmt.Errorf("rule page outside this cycle")
	}
	r := c.Rules[page-1]
	body, err := os.ReadFile(r.Path)
	if err != nil {
		return r, nil, err
	}
	if r.Start < 0 || r.End > len(body) || r.End < r.Start || ruleDigest(id, page, body[r.Start:r.End]) != r.Digest {
		return r, nil, fmt.Errorf("rule source changed; start a fresh plan before business work")
	}
	return r, body[r.Start:r.End], nil
}

func AcknowledgeCycleRule(scope Scope, id string, page int, digest string) error {
	r, _, err := ReadCycleRule(scope, id, page)
	if err != nil {
		return err
	}
	if digest != r.Digest {
		return fmt.Errorf("rule receipt does not match this cycle page")
	}
	return update(scope, func(st *State) error {
		c, ok := st.Cycles[id]
		if !ok || c.Result != "" || page > len(c.Rules) || c.Rules[page-1].Digest != digest {
			return fmt.Errorf("cycle rule unavailable")
		}
		c.Rules[page-1].Acknowledged = true
		st.Cycles[id] = c
		return nil
	})
}

func CycleMissing(c Cycle) (rules []int, stages []string) {
	rules = []int{}
	stages = []string{}
	for i, r := range c.Rules {
		if !r.Acknowledged {
			rules = append(rules, i+1)
		}
	}
	for stage, result := range c.Stages {
		if result != "completed" {
			stages = append(stages, stage)
		}
	}
	sort.Strings(stages)
	return
}

func BindStopCycle(scope Scope, session, id string) error {
	return update(scope, func(st *State) error {
		if _, ok := st.Cycles[id]; !ok {
			return fmt.Errorf("unknown cycle")
		}
		if st.GuardedCycles == nil {
			st.GuardedCycles = map[string]string{}
		}
		if len(st.GuardedCycles) >= 32 {
			if _, ok := st.GuardedCycles[session]; !ok {
				return fmt.Errorf("heartbeat host binding capacity reached")
			}
		}
		st.GuardedCycles[session] = id
		return nil
	})
}
func StopCycle(scope Scope, session string) (string, error) {
	st, err := Snapshot(scope)
	if err != nil {
		return "", err
	}
	return st.GuardedCycles[session], nil
}
