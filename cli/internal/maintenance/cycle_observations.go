package maintenance

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

var observationDiagnosticOnce sync.Once

func observationRecoveryDiagnostic() {
	observationDiagnosticOnce.Do(func() {
		fmt.Fprintln(os.Stderr, "[eigenflux:maintenance] component=runtime phase=observation_recovery error_code=observation_state_unavailable")
	})
}

// Observation receipts live outside State so older CLI state writers cannot erase
// deduplication or revive an observation after its upload was acknowledged.
type observationReceipt struct {
	Event        Event `json:"event"`
	Acknowledged bool  `json:"acknowledged,omitempty"`
}
type observationReceipts map[string]observationReceipt

func readObservationReceipts(scope Scope) (observationReceipts, error) {
	path, err := gatePath(scope, "observations")
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return observationReceipts{}, nil
	}
	if err != nil {
		return nil, err
	}
	var receipts observationReceipts
	if err := json.Unmarshal(body, &receipts); err != nil {
		return nil, fmt.Errorf("invalid observation receipts: %w", err)
	}
	if receipts == nil {
		receipts = observationReceipts{}
	}
	cutoff := time.Now().Add(-Retention).UnixMilli()
	for id, receipt := range receipts {
		if receipt.Event.EventID != id || Validate(receipt.Event) != nil {
			return nil, fmt.Errorf("invalid observation receipt")
		}
		if receipt.Event.EventAt < cutoff {
			delete(receipts, id)
		}
	}
	return receipts, nil
}

// recordObservation holds the existing account state lock for both files. The
// sidecar retains the event itself so a failed State write remains recoverable.
func recordObservation(scope Scope, st *State, event Event) error {
	if err := Validate(event); err != nil {
		return err
	}
	receipts, err := readObservationReceipts(scope)
	if err != nil {
		return err
	}
	if _, exists := receipts[event.EventID]; exists {
		return nil
	}
	if len(receipts) >= MaxEvents*4 {
		return fmt.Errorf("observation receipt capacity reached")
	}
	receipts[event.EventID] = observationReceipt{Event: event}
	if err := writeGateJSON(scope, "observations", receipts); err != nil {
		return err
	}
	for _, queued := range st.Events {
		if queued.EventID == event.EventID {
			return nil
		}
	}
	st.Events = append(st.Events, event)
	return nil
}

// ObserveCycle records a nonterminal observation, never a business-stage receipt.
// Repeated calls remain idempotent after upload and across legacy State writes.
func ObserveCycle(scope Scope, id, result string) error {
	if !idValue.MatchString(id) {
		return fmt.Errorf("invalid cycle identity")
	}
	switch result {
	case "plan_created", "incomplete", "permission_required", "interrupted":
	default:
		return fmt.Errorf("invalid cycle observation")
	}
	return update(scope, func(st *State) error {
		cycle, ok := st.Cycles[id]
		if !ok {
			return fmt.Errorf("unknown or expired cycle for this account")
		}
		return recordObservation(scope, st, cycleEvent(cycle, "cycle", result))
	})
}

// RecordLifecycle accepts fixed categories and token-only environment evidence.
// It cannot establish cycle execution, completion, or an authorization grant.
func RecordLifecycle(scope Scope, attempt, stage, result string, env RuntimeObservation) error {
	if !idValue.MatchString(attempt) {
		return fmt.Errorf("invalid lifecycle identity")
	}
	env.Stage, env.PlanMode = stage, ""
	event := NewEvent(attempt, "runtime", "auto", "lifecycle", result)
	// Different lifecycle stages may intentionally use the same result.
	event.EventID = cycleEvent(Cycle{ID: attempt}, "lifecycle."+stage, result).EventID
	event.Runtime = &env
	return update(scope, func(st *State) error { return recordObservation(scope, st, event) })
}

// reconcileObservationQueue restores pending events after legacy writes and
// removes acknowledged events resurrected by a stale writer. Call under update.
func reconcileObservationQueue(scope Scope, st *State, acknowledged map[string]bool) error {
	receipts, err := readObservationReceipts(scope)
	if err != nil {
		return err
	}
	changed := false
	for id := range acknowledged {
		receipt, ok := receipts[id]
		if ok && !receipt.Acknowledged {
			receipt.Acknowledged = true
			receipts[id] = receipt
			changed = true
		}
	}
	if changed {
		// Persist ACKs before removing State rows. A crash can leave stale rows, but
		// the next flush removes them without sending an acknowledged event again.
		if err := writeGateJSON(scope, "observations", receipts); err != nil {
			return err
		}
	}
	queued := map[string]bool{}
	kept := st.Events[:0]
	for _, event := range st.Events {
		if receipt, ok := receipts[event.EventID]; ok && receipt.Acknowledged {
			continue
		}
		kept = append(kept, event)
		queued[event.EventID] = true
	}
	st.Events = kept
	pending := []Event{}
	for id, receipt := range receipts {
		if !receipt.Acknowledged && !queued[id] {
			pending = append(pending, receipt.Event)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].EventAt == pending[j].EventAt {
			return pending[i].EventID < pending[j].EventID
		}
		return pending[i].EventAt < pending[j].EventAt
	})
	st.Events = append(st.Events, pending...)
	return nil
}
