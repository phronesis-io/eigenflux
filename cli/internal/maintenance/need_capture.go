package maintenance

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrNeedCaptureSelection denotes invalid arguments or a known selection mismatch,
// not a storage failure. The caller must reject it before submitting to the server.
var ErrNeedCaptureSelection = errors.New("invalid Need capture selection")

// Need capture receipts stay local. They contain no Intent text or Need payloads
// and are not uploaded as new server event types.
type NeedCaptureItem struct {
	IntentID      string `json:"intent_id"`
	IntentVersion int64  `json:"intent_version"`
	Status        string `json:"status"`
	Outcome       string `json:"outcome,omitempty"`
	ErrorCode     string `json:"error_code,omitempty"`
}

type NeedCaptureRun struct {
	ID            string            `json:"plan_id"`
	CreatedAt     int64             `json:"created_at"`
	CLIVersion    string            `json:"cli_version"`
	SkillRevision string            `json:"skill_revision"`
	QueryStatus   string            `json:"query_status"`
	ErrorCode     string            `json:"error_code,omitempty"`
	Items         []NeedCaptureItem `json:"items"`
}

func SaveNeedCaptureRun(scope Scope, run NeedCaptureRun) error {
	if !idValue.MatchString(run.ID) || run.CreatedAt <= 0 || len(run.Items) > 2 ||
		!contains([]string{"ready", "empty", "failed"}, run.QueryStatus) || !safeValue.MatchString(run.ErrorCode) {
		return fmt.Errorf("invalid Need capture plan")
	}
	if (run.QueryStatus == "ready") != (len(run.Items) > 0) {
		return fmt.Errorf("Need capture selection does not match query status")
	}
	seen := map[string]bool{}
	for _, item := range run.Items {
		id, err := strconv.ParseInt(item.IntentID, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != item.IntentID || item.IntentVersion <= 0 ||
			item.Status != "pending" || item.Outcome != "" || item.ErrorCode != "" || seen[item.IntentID] {
			return fmt.Errorf("invalid selected Intent")
		}
		seen[item.IntentID] = true
	}
	return updateNeedCapture(scope, func(st *State) error {
		if st.NeedCaptures == nil {
			st.NeedCaptures = map[string]NeedCaptureRun{}
		}
		if _, exists := st.NeedCaptures[run.ID]; exists {
			return fmt.Errorf("Need capture plan already exists")
		}
		st.NeedCaptures[run.ID] = run
		return nil
	})
}

func ReadNeedCaptureRun(scope Scope, id string) (NeedCaptureRun, error) {
	var run NeedCaptureRun
	if !idValue.MatchString(id) {
		return run, fmt.Errorf("invalid Need capture plan ID")
	}
	err := updateNeedCapture(scope, func(st *State) error {
		var ok bool
		run, ok = st.NeedCaptures[id]
		if !ok {
			return fmt.Errorf("unknown or expired Need capture plan for this account")
		}
		return nil
	})
	return run, err
}

func RecordNeedCaptureResult(scope Scope, plan, intent string, version int64, status, outcome, code string) error {
	if !idValue.MatchString(plan) || !contains([]string{"unconfirmed", "failed", "confirmed"}, status) ||
		!contains([]string{"captured", "no_need"}, outcome) || !safeValue.MatchString(code) || status == "confirmed" && code != "" {
		return fmt.Errorf("%w: invalid capture result", ErrNeedCaptureSelection)
	}
	return updateNeedCapture(scope, func(st *State) error {
		run, ok := st.NeedCaptures[plan]
		if !ok {
			return fmt.Errorf("unknown or expired Need capture plan for this account")
		}
		for i := range run.Items {
			item := &run.Items[i]
			if item.IntentID != intent || item.IntentVersion != version {
				continue
			}
			// An uncertain or failed retry cannot erase an observed successful save.
			if item.Status == "confirmed" {
				return nil
			}
			item.Status, item.Outcome, item.ErrorCode = status, outcome, code
			st.NeedCaptures[plan] = run
			return nil
		}
		return fmt.Errorf("%w: Intent version was not selected in this plan", ErrNeedCaptureSelection)
	})
}

func pruneNeedCaptures(st *State, now time.Time) {
	runs := make([]NeedCaptureRun, 0, len(st.NeedCaptures))
	for id, run := range st.NeedCaptures {
		if run.CreatedAt < now.Add(-Retention).UnixMilli() {
			delete(st.NeedCaptures, id)
		} else {
			runs = append(runs, run)
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].CreatedAt == runs[j].CreatedAt {
			return runs[i].ID < runs[j].ID
		}
		return runs[i].CreatedAt < runs[j].CreatedAt
	})
	if len(runs) > 64 {
		for _, run := range runs[:len(runs)-64] {
			delete(st.NeedCaptures, run.ID)
		}
	}
}

// Legacy CLI/watch processes rewrite the shared event state through a struct that
// cannot preserve new fields. Keep capture receipts in their own account-scoped
// file so those processes never read or overwrite them.
func updateNeedCapture(scope Scope, fn func(*State) error) error {
	path, err := scope.path()
	if err != nil {
		return err
	}
	return updatePath(strings.TrimSuffix(path, "-events.json")+"-need-capture.json", fn)
}

// NeedCaptureStrikeLimit is the number of retained plans in which one Intent
// version may be selected without a confirmed submission before planning skips it.
const NeedCaptureStrikeLimit = 3

// NeedCaptureHistory summarizes retained local plans. strikes counts, per
// "intent_id@version", plans that selected the version without confirming it.
// hasPrior reports whether any Need plan has been retained for this account.
// Retention bounds both: expired receipts restore eligibility.
func NeedCaptureHistory(scope Scope) (strikes map[string]int, hasPrior bool, err error) {
	strikes = map[string]int{}
	err = updateNeedCapture(scope, func(st *State) error {
		hasPrior = len(st.NeedCaptures) > 0
		for _, run := range st.NeedCaptures {
			for _, item := range run.Items {
				if item.Status != "confirmed" {
					strikes[NeedCaptureKey(item.IntentID, item.IntentVersion)]++
				}
			}
		}
		return nil
	})
	return strikes, hasPrior, err
}

func NeedCaptureKey(intent string, version int64) string {
	return intent + "@" + strconv.FormatInt(version, 10)
}
