package dispatch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func validCommissionResult(requestID, outcome string) CommissionIntakeDecision {
	return CommissionIntakeDecision{Version: 1, RequestID: requestID, OrderID: "9007199254740993", OrderVersion: 4, Outcome: outcome, Summary: "材料检查完成", InspectedFiles: []string{"inputs/request.txt"}}
}

func TestParseCommissionIntakeDecisionStrictContract(t *testing.T) {
	valid := validCommissionResult("request", "ready")
	raw, _ := json.Marshal(valid)
	for _, outcome := range []string{"ready", "needs_input", "needs_user", "failed"} {
		result := validCommissionResult("request", outcome)
		result.InspectedFiles = []string{}
		body, _ := json.Marshal(result)
		got, err := ParseCommissionIntakeDecision(string(body), "request", valid.OrderID, 4)
		if err != nil || !reflect.DeepEqual(result, got) {
			t.Fatalf("valid %s result rejected: %+v %v", outcome, got, err)
		}
	}
	for _, field := range []string{"version", "request_id", "order_id", "order_version", "outcome", "summary", "inspected_files"} {
		for _, missing := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/missing=%t", field, missing), func(t *testing.T) {
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(raw, &fields)
				if missing {
					delete(fields, field)
				} else {
					fields[field] = json.RawMessage("null")
				}
				body, _ := json.Marshal(fields)
				if _, err := ParseCommissionIntakeDecision(string(body), "request", valid.OrderID, 4); err == nil {
					t.Fatalf("missing/null %s accepted", field)
				}
			})
		}
	}
	for name, body := range map[string]string{
		"unknown field":   strings.TrimSuffix(string(raw), "}") + `,"accept":true}`,
		"duplicate field": strings.TrimSuffix(string(raw), "}") + `,"outcome":"ready"}`,
		"case duplicate":  strings.TrimSuffix(string(raw), "}") + `,"Outcome":"ready"}`,
		"second object":   string(raw) + " {}",
		"markdown":        "```json\n" + string(raw) + "\n```",
		"bad utf8":        string(raw) + string([]byte{0xff}),
		"too large":       string(raw) + strings.Repeat(" ", 1<<20),
		"array":           "[]", "null": "null",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCommissionIntakeDecision(body, "request", valid.OrderID, 4); err == nil {
				t.Fatal("invalid envelope accepted")
			}
		})
	}
	for name, change := range map[string]func(*CommissionIntakeDecision){
		"version":        func(v *CommissionIntakeDecision) { v.Version = 2 },
		"request":        func(v *CommissionIntakeDecision) { v.RequestID = "another" },
		"order":          func(v *CommissionIntakeDecision) { v.OrderID = "9007199254740994" },
		"old version":    func(v *CommissionIntakeDecision) { v.OrderVersion = 3 },
		"future version": func(v *CommissionIntakeDecision) { v.OrderVersion = 5 },
		"accept outcome": func(v *CommissionIntakeDecision) { v.Outcome = "accepted" },
		"blank summary":  func(v *CommissionIntakeDecision) { v.Summary = " \n " },
		"large summary":  func(v *CommissionIntakeDecision) { v.Summary = strings.Repeat("界", 667) },
		"nil files":      func(v *CommissionIntakeDecision) { v.InspectedFiles = nil },
		"many files":     func(v *CommissionIntakeDecision) { v.InspectedFiles = make([]string, 129) },
		"blank file":     func(v *CommissionIntakeDecision) { v.InspectedFiles = []string{" "} },
		"long file":      func(v *CommissionIntakeDecision) { v.InspectedFiles = []string{strings.Repeat("x", 1025)} },
		"control file":   func(v *CommissionIntakeDecision) { v.InspectedFiles = []string{"inputs/\u0085request.txt"} },
		"duplicate file": func(v *CommissionIntakeDecision) { v.InspectedFiles = []string{"a", "a"} },
	} {
		t.Run(name, func(t *testing.T) {
			result := validCommissionResult("request", "ready")
			change(&result)
			body, _ := json.Marshal(result)
			if _, err := ParseCommissionIntakeDecision(string(body), "request", valid.OrderID, 4); err == nil {
				t.Fatal("invalid decision accepted")
			}
		})
	}
	valid.Summary = strings.Repeat("x", 2000)
	valid.InspectedFiles = make([]string, 128)
	for i := range valid.InspectedFiles {
		valid.InspectedFiles[i] = fmt.Sprintf("%03d%s", i, strings.Repeat("x", 1021))
	}
	raw, _ = json.Marshal(valid)
	if _, err := ParseCommissionIntakeDecision(string(raw), "request", valid.OrderID, 4); err != nil {
		t.Fatalf("boundary-sized decision rejected: %v", err)
	}
}

func runningCommissionIntake(t *testing.T) (*Journal, Job) {
	t.Helper()
	j := commissionJournal(t)
	mustAddCommission(t, j, commissionNotification(9007199254740993, 3, "seller", false))
	job, ok, err := j.NextKind("commission_order")
	if err != nil || !ok {
		t.Fatalf("claim commission: %v %v", ok, err)
	}
	return j, job
}

func TestCompleteCommissionIntakePersistsResultsAndIsolatesCopies(t *testing.T) {
	for outcome, status := range map[string]string{"ready": "completed", "needs_input": "needs_user", "needs_user": "needs_user", "failed": "failed"} {
		t.Run(outcome, func(t *testing.T) {
			j, job := runningCommissionIntake(t)
			result := validCommissionResult(job.ID, outcome)
			if err := j.CompleteCommissionIntake(job.ID, result, "session"); err != nil {
				t.Fatal(err)
			}
			result.InspectedFiles[0] = "caller-mutated"
			copy := cloneJournal(j.state)
			copy.Jobs[0].CommissionResult.Summary = "copy-mutated"
			copy.Jobs[0].CommissionResult.InspectedFiles[0] = "copy-mutated"
			snapshot := j.Snapshot()
			snapshot[0].CommissionResult.Summary = "snapshot-mutated"
			snapshot[0].CommissionResult.InspectedFiles[0] = "snapshot-mutated"
			got := j.state.Jobs[0]
			if got.Status != status || got.Code != "commission_intake_"+outcome || got.SessionID != "session" || !reflect.DeepEqual(*got.CommissionResult, validCommissionResult(job.ID, outcome)) {
				t.Fatalf("incorrect committed result: %+v", got)
			}
			if (len(got.Data) == 0) != (outcome == "ready") {
				t.Fatal("incorrect result compaction")
			}
			j = mustJournal(t, j.binding)
			statusRows, err := ReadJournalStatus(j.binding)
			if err != nil || len(statusRows) != 1 || statusRows[0].Data != nil || !reflect.DeepEqual(statusRows[0].CommissionResult, got.CommissionResult) {
				t.Fatalf("result missing from persisted status: %+v %v", statusRows, err)
			}
			if err := j.CompleteCommissionIntake(job.ID, validCommissionResult(job.ID, outcome), ""); err == nil {
				t.Fatal("non-running completion accepted")
			}
		})
	}
}

func TestCompleteCommissionIntakeRejectsMismatchesAndRollsBack(t *testing.T) {
	j, job := runningCommissionIntake(t)
	before := cloneJournal(j.state)
	for _, result := range []CommissionIntakeDecision{
		validCommissionResult("wrong-request", "ready"),
		{Version: 1, RequestID: job.ID, OrderID: "9007199254740994", OrderVersion: 4, Outcome: "ready", Summary: "checked", InspectedFiles: []string{}},
		{Version: 1, RequestID: job.ID, OrderID: "9007199254740993", OrderVersion: 2, Outcome: "ready", Summary: "checked", InspectedFiles: []string{}},
	} {
		if err := j.CompleteCommissionIntake(job.ID, result, "session"); err == nil || !reflect.DeepEqual(before, j.state) {
			t.Fatal("mismatched result accepted or changed memory")
		}
	}
	path := j.path
	j.path = filepath.Join(t.TempDir(), "blocked", "journal.json")
	if err := os.WriteFile(filepath.Dir(j.path), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteCommissionIntake(job.ID, validCommissionResult(job.ID, "ready"), "session"); err == nil || !reflect.DeepEqual(before, j.state) {
		t.Fatal("failed result save changed memory")
	}
	j.path = path
	rows, err := ReadJournalStatus(j.binding)
	if err != nil || rows[0].Status != "running" || rows[0].CommissionResult != nil {
		t.Fatal("failed result save changed persisted state")
	}
	pm := mustJournal(t, journalBinding(t))
	mustAdd(t, pm, "one")
	pmJob := mustNext(t, pm)
	if err := pm.CompleteCommissionIntake(pmJob.ID, validCommissionResult(pmJob.ID, "ready"), ""); err == nil {
		t.Fatal("PM accepted commission result")
	}
}

func TestCommissionIntakeRetryAndReconcileRetainTheirMeaning(t *testing.T) {
	for _, reconcile := range []string{"", "completed", "failed"} {
		t.Run("reconcile="+reconcile, func(t *testing.T) {
			j, job := runningCommissionIntake(t)
			result := validCommissionResult(job.ID, "needs_input")
			if err := j.CompleteCommissionIntake(job.ID, result, ""); err != nil {
				t.Fatal(err)
			}
			if reconcile != "" {
				if err := j.Reconcile(job.ID, reconcile, ""); err != nil {
					t.Fatal(err)
				}
				j = mustJournal(t, j.binding)
				got := j.Snapshot()[0]
				if got.Code != "operator_verified" || got.Status != reconcile || !reflect.DeepEqual(*got.CommissionResult, result) {
					t.Fatal("reconciliation rewrote model outcome or lost operator provenance")
				}
			}
			if reconcile != "completed" {
				if err := j.Retry(job.ID); err != nil {
					t.Fatal(err)
				}
				j = mustJournal(t, j.binding)
				if got := j.Snapshot()[0]; got.Status != "pending" || got.Code != "operator_retry" || got.CommissionResult != nil {
					t.Fatal("retry retained stale model result")
				}
			}
		})
	}
}

func TestCommissionIntakeReadRejectsInvalidPersistedResults(t *testing.T) {
	for name, mutate := range map[string]func(*Job){
		"request":         func(j *Job) { j.CommissionResult.RequestID = "other" },
		"order":           func(j *Job) { j.CommissionResult.OrderID = "9007199254740994" },
		"old version":     func(j *Job) { j.CommissionResult.OrderVersion = 2 },
		"status":          func(j *Job) { j.Status = "completed" },
		"code":            func(j *Job) { j.Code = "commission_intake_ready" },
		"active result":   func(j *Job) { j.Status = "running" },
		"outcome":         func(j *Job) { j.CommissionResult.Outcome = "accepted" },
		"nil files":       func(j *Job) { j.CommissionResult.InspectedFiles = nil },
		"duplicate files": func(j *Job) { j.CommissionResult.InspectedFiles = []string{"a", "a"} },
		"blank summary":   func(j *Job) { j.CommissionResult.Summary = " " },
	} {
		t.Run(name, func(t *testing.T) {
			j, job := runningCommissionIntake(t)
			if err := j.CompleteCommissionIntake(job.ID, validCommissionResult(job.ID, "needs_input"), ""); err != nil {
				t.Fatal(err)
			}
			mutate(&j.state.Jobs[0])
			if err := WriteJSON(j.path, j.state); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadJournalStatus(j.binding); err == nil {
				t.Fatal("invalid persisted result accepted")
			}
		})
	}
	// Historical jobs without a result remain readable; opening a journal must
	// not synthesize a successful inspection for old or reconciled work.
	j, job := runningCommissionIntake(t)
	if err := j.Update(job.ID, "completed", "legacy", "", ""); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, j.binding)
	if got := j.Snapshot()[0]; got.CommissionResult != nil || got.Code != "legacy" {
		t.Fatal("legacy result was fabricated")
	}
}
