package dispatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func validFulfillmentDecision(requestID string) CommissionFulfillmentDecision {
	return CommissionFulfillmentDecision{Version: 1, RequestID: requestID, OrderID: "9007199254740993", OrderVersion: 4, Outcome: "artifacts_ready", Summary: "本地产物已就绪", SelfCheck: "已检查报告格式和合同要求", Artifacts: []CommissionArtifact{{LogicalPath: "outputs/report.md", RelativePath: "report.md"}}}
}

func TestCommissionFulfillmentDecisionStrictContract(t *testing.T) {
	valid := validFulfillmentDecision("request")
	raw, _ := json.Marshal(valid)
	for _, outcome := range []string{"artifacts_ready", "needs_input", "needs_user", "failed"} {
		decision := validFulfillmentDecision("request")
		decision.Outcome = outcome
		if outcome != "artifacts_ready" {
			decision.Artifacts = []CommissionArtifact{}
			decision.SelfCheck = ""
		}
		body, _ := json.Marshal(decision)
		got, err := ParseCommissionFulfillmentDecision(string(body), "request", valid.OrderID, 4)
		if err != nil || !reflect.DeepEqual(got, decision) {
			t.Fatalf("valid outcome %s: %+v %v", outcome, got, err)
		}
	}
	for _, name := range []string{"version", "request_id", "order_id", "order_version", "outcome", "summary", "self_check", "artifacts"} {
		for _, missing := range []bool{false, true} {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(raw, &fields)
			if missing {
				delete(fields, name)
			} else {
				fields[name] = json.RawMessage("null")
			}
			body, _ := json.Marshal(fields)
			if _, err := ParseCommissionFulfillmentDecision(string(body), "request", valid.OrderID, 4); err == nil {
				t.Fatalf("missing/null %s accepted", name)
			}
		}
	}
	for name, change := range map[string]func(*CommissionFulfillmentDecision){
		"protocol":       func(v *CommissionFulfillmentDecision) { v.Version = 2 },
		"request":        func(v *CommissionFulfillmentDecision) { v.RequestID = "other" },
		"order":          func(v *CommissionFulfillmentDecision) { v.OrderID = "901" },
		"order version":  func(v *CommissionFulfillmentDecision) { v.OrderVersion++ },
		"outcome":        func(v *CommissionFulfillmentDecision) { v.Outcome = "delivered" },
		"empty summary":  func(v *CommissionFulfillmentDecision) { v.Summary = " " },
		"long summary":   func(v *CommissionFulfillmentDecision) { v.Summary = strings.Repeat("界", 667) },
		"long check":     func(v *CommissionFulfillmentDecision) { v.SelfCheck = strings.Repeat("a", 2001) },
		"empty check":    func(v *CommissionFulfillmentDecision) { v.SelfCheck = "\n" },
		"nil artifacts":  func(v *CommissionFulfillmentDecision) { v.Artifacts = nil },
		"empty ready":    func(v *CommissionFulfillmentDecision) { v.Artifacts = []CommissionArtifact{} },
		"many artifacts": func(v *CommissionFulfillmentDecision) { v.Artifacts = make([]CommissionArtifact, 129) },
		"duplicate logical": func(v *CommissionFulfillmentDecision) {
			v.Artifacts = append(v.Artifacts, CommissionArtifact{LogicalPath: "outputs/report.md", RelativePath: "second.md"})
		},
		"duplicate physical": func(v *CommissionFulfillmentDecision) {
			v.Artifacts = append(v.Artifacts, CommissionArtifact{LogicalPath: "outputs/other.md", RelativePath: "report.md"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			decision := validFulfillmentDecision("request")
			change(&decision)
			body, _ := json.Marshal(decision)
			if _, err := ParseCommissionFulfillmentDecision(string(body), "request", valid.OrderID, 4); err == nil {
				t.Fatal("invalid result accepted")
			}
		})
	}
	for _, invalid := range []string{"", " ", ".", "..", "../report", "a/../report", "/report", "a//report", "./report", "a/", `a\report`, "C:/report", "a\x00b", "a\u0085b", strings.Repeat("a", 1025)} {
		for _, logical := range []bool{true, false} {
			decision := validFulfillmentDecision("request")
			if logical {
				decision.Artifacts[0].LogicalPath = invalid
			} else {
				decision.Artifacts[0].RelativePath = invalid
			}
			body, _ := json.Marshal(decision)
			if _, err := ParseCommissionFulfillmentDecision(string(body), "request", valid.OrderID, 4); err == nil {
				t.Fatalf("unsafe path %q accepted", invalid)
			}
		}
	}
	for _, invalid := range []string{string(raw) + " {}", strings.TrimSuffix(string(raw), "}") + `,"outcome":"artifacts_ready"}`, strings.TrimSuffix(string(raw), "}") + `,"extra":true}`, strings.Replace(string(raw), `"relative_path":"report.md"`, `"relative_path":"report.md","extra":true`, 1), string(raw) + string([]byte{0xff}), string(raw) + strings.Repeat(" ", 1<<20)} {
		if _, err := ParseCommissionFulfillmentDecision(invalid, "request", valid.OrderID, 4); err == nil {
			t.Fatal("invalid JSON contract accepted")
		}
	}
}

func queueFulfillment(t *testing.T) (*Journal, Job) {
	t.Helper()
	j, intake := runningCommissionIntake(t)
	if err := j.CompleteCommissionIntakeAndQueue(intake.ID, validCommissionResult(intake.ID, "ready"), "input-session", true); err != nil {
		t.Fatal(err)
	}
	for _, job := range j.state.Jobs {
		if job.Kind == "commission_fulfillment" {
			return j, cloneJob(job)
		}
	}
	t.Fatal("fulfillment not queued")
	return nil, Job{}
}

func claimFulfillment(t *testing.T, j *Journal) Job {
	t.Helper()
	job, ok, err := j.NextKind("commission_fulfillment")
	if err != nil || !ok {
		t.Fatalf("claim fulfillment: %v %v", ok, err)
	}
	return job
}

func preparedFulfillment(t *testing.T) (*Journal, Job, CommissionFulfillmentResult) {
	t.Helper()
	j, _ := queueFulfillment(t)
	job := claimFulfillment(t, j)
	directory := t.TempDir()
	if err := j.SetCommissionFulfillmentDirectory(job.ID, directory); err != nil {
		t.Fatal(err)
	}
	result := CommissionFulfillmentResult{Decision: validFulfillmentDecision(job.ID), OutputDirectory: directory, Artifacts: []CommissionVerifiedArtifact{{LogicalPath: "outputs/report.md", RelativePath: "report.md", SHA256: strings.Repeat("a", 64), ByteSize: 42}}}
	return j, job, result
}

func TestCommissionFulfillmentQueueAtomicAndDeduplicated(t *testing.T) {
	for _, paid := range []bool{false, true} {
		for _, outcome := range []string{"ready", "needs_input", "needs_user", "failed"} {
			j, intake := runningCommissionIntake(t)
			result := validCommissionResult(intake.ID, outcome)
			if err := j.CompleteCommissionIntakeAndQueue(intake.ID, result, "session", paid); err != nil {
				t.Fatal(err)
			}
			result.InspectedFiles[0] = "caller changed"
			j = mustJournal(t, j.binding)
			count := 0
			for _, job := range j.state.Jobs {
				if job.Kind == "commission_fulfillment" {
					count++
					var payload CommissionFulfillmentJob
					if json.Unmarshal(job.Data, &payload) != nil || payload.IntakeRequestID != intake.ID || payload.IntakeResult.InspectedFiles[0] != "inputs/request.txt" {
						t.Fatal("queue lost immutable intake evidence")
					}
				}
			}
			if (count == 1) != (paid && outcome == "ready") {
				t.Fatalf("paid=%v outcome=%s count=%d", paid, outcome, count)
			}
		}
	}
	j, queued := queueFulfillment(t)
	mustAddCommission(t, j, commissionNotification(9007199254740993, 6, "seller", false))
	intake, ok, err := j.NextKind("commission_order")
	if err != nil || !ok {
		t.Fatal(err)
	}
	result := validCommissionResult(intake.ID, "ready")
	result.OrderVersion = 6
	if err := j.CompleteCommissionIntakeAndQueue(intake.ID, result, "", true); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, job := range j.state.Jobs {
		if job.Kind == "commission_fulfillment" {
			count++
			if !reflect.DeepEqual(job, queued) {
				t.Fatal("later notification replaced the queued job")
			}
		}
	}
	if count != 1 {
		t.Fatal("order versions duplicated fulfillment")
	}
	// Simulate completed intake eviction: the queue retains its own ready evidence.
	j.state.Jobs = []Job{queued}
	if err := WriteJSON(j.path, j.state); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, j.binding)
	if _, ok, err := j.Next(); ok || err != nil {
		t.Fatal("general worker claimed fulfillment")
	}
	_ = claimFulfillment(t, j)
	if err := j.AddHint("commission_fulfillment", json.RawMessage(`{}`)); err == nil {
		t.Fatal("raw hints can dispatch fulfillment")
	}
}

func TestCommissionFulfillmentQueuePersistenceRollback(t *testing.T) {
	j, intake := runningCommissionIntake(t)
	original := j.path
	j.path = filepath.Join(t.TempDir(), "blocked", "journal.json")
	if err := os.WriteFile(filepath.Dir(j.path), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	before := cloneJournal(j.state)
	if err := j.CompleteCommissionIntakeAndQueue(intake.ID, validCommissionResult(intake.ID, "ready"), "", true); err == nil || !reflect.DeepEqual(before, j.state) {
		t.Fatal("failed transaction partially completed intake or queued work")
	}
	j.path = original
	rows, err := ReadJournalStatus(j.binding)
	if err != nil || len(rows) != 1 || rows[0].Status != "running" || rows[0].CommissionResult != nil {
		t.Fatal("failed transaction changed disk")
	}
}

func TestCommissionFulfillmentThreeWorkersRemainIndependent(t *testing.T) {
	j, _ := queueFulfillment(t)
	mustAddCommission(t, j, commissionNotification(9007199254740994, 3, "seller", false))
	batch, _ := json.Marshal(map[string]any{"messages": []Message{{ID: "pm", Conversation: "conv", Sender: "peer", Receiver: j.binding.AgentID}}})
	if err := j.AddMessages(batch); err != nil {
		t.Fatal(err)
	}
	fulfillment := claimFulfillment(t, j)
	intake, ok, err := j.NextKind("commission_order")
	if err != nil || !ok {
		t.Fatal("fulfillment blocked intake")
	}
	pm := mustNext(t, j)
	if pm.Kind != "pm_push" || intake.Kind != "commission_order" || fulfillment.Kind != "commission_fulfillment" {
		t.Fatal("workers crossed event kinds")
	}
	for _, kind := range []string{"commission_order", "commission_fulfillment"} {
		if _, ok, err := j.NextKind(kind); ok || err != nil {
			t.Fatal("worker claimed simultaneous job of same kind")
		}
	}
}

func TestCommissionFulfillmentResultPersistenceRetryAndReconciliation(t *testing.T) {
	j, job, result := preparedFulfillment(t)
	if err := j.CompleteCommissionFulfillment(job.ID, result, "fulfillment-session"); err != nil {
		t.Fatal(err)
	}
	result.Decision.Artifacts[0].LogicalPath = "caller changed"
	result.Artifacts[0].SHA256 = "caller changed"
	copy := j.Snapshot()
	for i := range copy {
		if copy[i].CommissionFulfillment != nil {
			copy[i].CommissionFulfillment.Decision.Artifacts[0].RelativePath = "snapshot changed"
			copy[i].CommissionFulfillment.Artifacts[0].RelativePath = "snapshot changed"
		}
	}
	j = mustJournal(t, j.binding)
	var stored Job
	for _, candidate := range j.Snapshot() {
		if candidate.ID == job.ID {
			stored = candidate
		}
	}
	if stored.Status != "needs_user" || stored.Code != "commission_fulfillment_artifacts_ready" || stored.SessionID != "fulfillment-session" || stored.CommissionFulfillment.Artifacts[0].SHA256 != strings.Repeat("a", 64) || stored.CommissionFulfillment.Decision.Artifacts[0].RelativePath != "report.md" {
		t.Fatalf("bad stored result: %+v", stored)
	}
	if err := j.Reconcile(job.ID, "failed", ""); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, j.binding)
	for _, candidate := range j.Snapshot() {
		if candidate.ID == job.ID && (candidate.Code != "operator_verified" || candidate.CommissionFulfillment.Decision.Outcome != "artifacts_ready") {
			t.Fatal("reconcile fabricated a new model result")
		}
	}
	if err := j.Retry(job.ID); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, j.binding)
	claimed := claimFulfillment(t, j)
	if claimed.CommissionFulfillment != nil || claimed.CommissionDirectory != result.OutputDirectory || claimed.Code != "operator_retry" {
		t.Fatal("retry lost old output location or retained success")
	}
	result.Decision = validFulfillmentDecision(job.ID)
	result.Artifacts[0].SHA256 = strings.Repeat("a", 64)
	if err := j.CompleteCommissionFulfillment(job.ID, result, ""); err == nil {
		t.Fatal("retry completed against a stale directory without preparation")
	}
	newDirectory := t.TempDir()
	if err := j.SetCommissionFulfillmentDirectory(job.ID, newDirectory); err != nil {
		t.Fatal(err)
	}
	if err := j.SetCommissionFulfillmentDirectory(job.ID, t.TempDir()); err == nil {
		t.Fatal("running invocation replaced its prepared directory")
	}
	j = mustJournal(t, j.binding)
	for _, candidate := range j.Snapshot() {
		if candidate.ID == job.ID && (candidate.Status != "unknown" || candidate.CommissionDirectory != newDirectory) {
			t.Fatal("interruption lost output directory")
		}
	}
	if err := j.Retry(job.ID); err == nil {
		t.Fatal("unknown work retried automatically")
	}
}

func TestCommissionFulfillmentRejectsInvalidEvidenceAndPaths(t *testing.T) {
	for name, change := range map[string]func(*CommissionFulfillmentResult){
		"directory":         func(r *CommissionFulfillmentResult) { r.OutputDirectory = "relative" },
		"other request":     func(r *CommissionFulfillmentResult) { r.Decision.RequestID = "other" },
		"other order":       func(r *CommissionFulfillmentResult) { r.Decision.OrderID = "901" },
		"other version":     func(r *CommissionFulfillmentResult) { r.Decision.OrderVersion++ },
		"missing evidence":  func(r *CommissionFulfillmentResult) { r.Artifacts = nil },
		"logical mismatch":  func(r *CommissionFulfillmentResult) { r.Artifacts[0].LogicalPath = "outputs/other.md" },
		"relative mismatch": func(r *CommissionFulfillmentResult) { r.Artifacts[0].RelativePath = "other.md" },
		"digest":            func(r *CommissionFulfillmentResult) { r.Artifacts[0].SHA256 = strings.Repeat("z", 64) },
		"negative size":     func(r *CommissionFulfillmentResult) { r.Artifacts[0].ByteSize = -1 },
		"excess size":       func(r *CommissionFulfillmentResult) { r.Artifacts[0].ByteSize = (64 << 20) + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			j, job, result := preparedFulfillment(t)
			before := cloneJournal(j.state)
			change(&result)
			if err := j.CompleteCommissionFulfillment(job.ID, result, ""); err == nil || !reflect.DeepEqual(before, j.state) {
				t.Fatal("invalid evidence changed journal")
			}
		})
	}
	j, _ := queueFulfillment(t)
	job := claimFulfillment(t, j)
	for _, directory := range []string{"", "relative", filepath.Join(t.TempDir(), "child") + string(filepath.Separator) + "..", t.TempDir() + "\n"} {
		if err := j.SetCommissionFulfillmentDirectory(job.ID, directory); err == nil {
			t.Fatalf("invalid directory %q accepted", directory)
		}
	}
}

func TestCommissionFulfillmentReadRejectsCorruption(t *testing.T) {
	for name, mutate := range map[string]func(*Job){
		"status":           func(j *Job) { j.Status = "completed" },
		"code":             func(j *Job) { j.Code = "commission_fulfillment_failed" },
		"directory":        func(j *Job) { j.CommissionDirectory = "relative" },
		"result directory": func(j *Job) { j.CommissionFulfillment.OutputDirectory = "other" },
		"bad digest":       func(j *Job) { j.CommissionFulfillment.Artifacts[0].SHA256 = "bad" },
		"queue order": func(j *Job) {
			var p CommissionFulfillmentJob
			_ = json.Unmarshal(j.Data, &p)
			p.OrderID = "901"
			j.Data, _ = json.Marshal(p)
		},
		"unready source": func(j *Job) {
			var p CommissionFulfillmentJob
			_ = json.Unmarshal(j.Data, &p)
			p.IntakeResult.Outcome = "needs_input"
			j.Data, _ = json.Marshal(p)
		},
		"source request": func(j *Job) {
			var p CommissionFulfillmentJob
			_ = json.Unmarshal(j.Data, &p)
			p.IntakeResult.RequestID = "other"
			j.Data, _ = json.Marshal(p)
		},
	} {
		t.Run(name, func(t *testing.T) {
			j, job, result := preparedFulfillment(t)
			if err := j.CompleteCommissionFulfillment(job.ID, result, ""); err != nil {
				t.Fatal(err)
			}
			for i := range j.state.Jobs {
				if j.state.Jobs[i].ID == job.ID {
					mutate(&j.state.Jobs[i])
				}
			}
			if err := WriteJSON(j.path, j.state); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadJournalStatus(j.binding); err == nil {
				t.Fatal("corrupt fulfillment journal accepted")
			}
		})
	}
}

func TestCommissionFulfillmentDirectoryAndResultFailuresRollBack(t *testing.T) {
	for _, stage := range []string{"directory", "result"} {
		t.Run(stage, func(t *testing.T) {
			j, _ := queueFulfillment(t)
			job := claimFulfillment(t, j)
			directory := t.TempDir()
			if stage == "result" {
				if err := j.SetCommissionFulfillmentDirectory(job.ID, directory); err != nil {
					t.Fatal(err)
				}
			}
			before := cloneJournal(j.state)
			j.path = filepath.Join(t.TempDir(), "blocked", "journal.json")
			if err := os.WriteFile(filepath.Dir(j.path), []byte("blocked"), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			if stage == "directory" {
				err = j.SetCommissionFulfillmentDirectory(job.ID, directory)
			} else {
				decision := validFulfillmentDecision(job.ID)
				decision.Outcome = "needs_user"
				decision.Artifacts = []CommissionArtifact{}
				decision.SelfCheck = ""
				err = j.CompleteCommissionFulfillment(job.ID, CommissionFulfillmentResult{Decision: decision, OutputDirectory: directory, Artifacts: []CommissionVerifiedArtifact{}}, "session")
			}
			if err == nil || !reflect.DeepEqual(before, j.state) {
				t.Fatal("failed persistence changed in-memory state")
			}
			rows, err := ReadJournalStatus(j.binding)
			if err != nil {
				t.Fatal(err)
			}
			for _, candidate := range rows {
				if candidate.ID == job.ID && (candidate.Status != "running" || candidate.CommissionFulfillment != nil || (stage == "directory" && candidate.CommissionDirectory != "")) {
					t.Fatal("failed persistence changed disk")
				}
			}
		})
	}
}

func TestCommissionFulfillmentNonReadyAndReconciledResults(t *testing.T) {
	for _, outcome := range []string{"needs_input", "needs_user", "failed"} {
		j, job, result := preparedFulfillment(t)
		result.Decision.Outcome = outcome
		result.Decision.SelfCheck = ""
		result.Decision.Artifacts = []CommissionArtifact{}
		result.Artifacts = []CommissionVerifiedArtifact{}
		if err := j.CompleteCommissionFulfillment(job.ID, result, ""); err != nil {
			t.Fatal(err)
		}
		j = mustJournal(t, j.binding)
		for _, candidate := range j.Snapshot() {
			if candidate.ID == job.ID && candidate.Status != commissionFulfillmentStatus(outcome) {
				t.Fatal("incorrect unresolved outcome")
			}
		}
		if err := j.Reconcile(job.ID, "completed", ""); err != nil {
			t.Fatal(err)
		}
		j = mustJournal(t, j.binding)
		for _, candidate := range j.state.Jobs {
			if candidate.ID == job.ID && (len(candidate.Data) == 0 || candidate.CommissionFulfillment.Decision.Outcome != outcome || candidate.Code != "operator_verified") {
				t.Fatal("reconciliation dropped evidence or fabricated readiness")
			}
		}
	}
}

func TestCommissionFulfillmentReadRejectsMissingEvidenceFields(t *testing.T) {
	for _, replacement := range []string{`"byte_size":null`, `"unrelated_size":42`} {
		j, job, result := preparedFulfillment(t)
		if err := j.CompleteCommissionFulfillment(job.ID, result, ""); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(j.state)
		if err != nil {
			t.Fatal(err)
		}
		raw = []byte(strings.Replace(string(raw), `"byte_size":42`, replacement, 1))
		if err := os.WriteFile(j.path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadJournalStatus(j.binding); err == nil {
			t.Fatal("missing or null byte_size silently became a valid zero-byte artifact")
		}
	}
}
