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

func journalBinding(t *testing.T) Binding {
	t.Helper()
	return Binding{Home: t.TempDir(), Scope: "scope-a", Revision: "revision-a", AgentID: "self", Events: []string{"pm_push", "control_pending", "profile_review_due", "maintenance_due"}}
}

func mustJournal(t *testing.T, b Binding) *Journal {
	t.Helper()
	j, err := OpenJournal(b)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func inboundBatch(ids ...string) json.RawMessage {
	msgs := make([]Message, 0, len(ids))
	for _, id := range ids {
		msgs = append(msgs, Message{ID: id, Conversation: "conv-" + id, Sender: "peer", Receiver: "self", Content: "private body"})
	}
	raw, _ := json.Marshal(map[string]any{"messages": msgs})
	return raw
}

func mustAdd(t *testing.T, j *Journal, ids ...string) {
	t.Helper()
	if err := j.AddMessages(inboundBatch(ids...)); err != nil {
		t.Fatal(err)
	}
}

func mustNext(t *testing.T, j *Journal) Job {
	t.Helper()
	job, ok, err := j.Next()
	if err != nil || !ok {
		t.Fatalf("Next: ok=%v err=%v", ok, err)
	}
	return job
}

func TestJournalFiltersIdentityAndHistory(t *testing.T) {
	b := journalBinding(t)
	j := mustJournal(t, b)
	raw := json.RawMessage(`{"messages":[{"msg_id":"in","conv_id":"c","sender_id":"peer","receiver_id":"self","content":"secret"},{"msg_id":"out","conv_id":"c","sender_id":"self","receiver_id":"peer"},{"msg_id":"other","conv_id":"c","sender_id":"peer","receiver_id":"other"}],"history_messages":[{"msg_id":"old","conv_id":"c","sender_id":"peer","receiver_id":"self"}]}`)
	if err := j.AddMessages(raw); err != nil {
		t.Fatal(err)
	}
	if err := j.AddMessages(raw); err != nil {
		t.Fatal(err)
	}
	snap := j.Snapshot()
	if len(snap) != 1 || snap[0].Message.ID != "in" || snap[0].Message.Content != "" || snap[0].Data != nil {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	snap[0].Message.ID = "tampered"
	job := mustNext(t, j)
	if job.Message.ID != "in" || job.Message.Content != "secret" {
		t.Fatalf("incorrect job: %+v", job)
	}
	job.Message.Content = "tampered"
	if j.state.Jobs[0].Message.Content != "secret" {
		t.Fatal("Next returned mutable journal data")
	}
	if err := j.Update(job.ID, "no_reply", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := j.AddMessages(raw); err != nil {
		t.Fatal(err)
	}
	if len(j.Snapshot()) != 1 {
		t.Fatal("completed message was duplicated")
	}
	j = mustJournal(t, b)
	if err := j.AddMessages(raw); err != nil {
		t.Fatal(err)
	}
	if len(j.Snapshot()) != 1 {
		t.Fatal("duplicate after reopen")
	}
}

func TestJournalBatchAtomicValidation(t *testing.T) {
	j := mustJournal(t, journalBinding(t))
	for _, raw := range []string{`null`, `[]`, `{"messages":[{"msg_id":"good","conv_id":"c","sender_id":"peer","receiver_id":"self"},{"msg_id":"","conv_id":"c","sender_id":"peer","receiver_id":"self"}]}`} {
		if err := j.AddMessages(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
		if len(j.Snapshot()) != 0 {
			t.Fatal("partial batch saved")
		}
	}
}

func TestJournalSaveFailuresRollbackEveryMutation(t *testing.T) {
	b := journalBinding(t)
	j := mustJournal(t, b)
	mustAdd(t, j, "a")
	original := j.path
	j.path = filepath.Join(t.TempDir(), "blocked", "journal.json")
	if err := os.WriteFile(filepath.Dir(j.path), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	before := cloneJournal(j.state)
	if err := j.AddMessages(inboundBatch("b")); err == nil {
		t.Fatal("expected AddMessages disk error")
	}
	if _, _, err := j.Next(); err == nil {
		t.Fatal("expected Next disk error")
	}
	if err := j.AddHint("maintenance_due", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected AddHint disk error")
	}
	if !reflect.DeepEqual(before, j.state) {
		t.Fatal("failed write mutated journal")
	}
	j.path = original
	job := mustNext(t, j)
	j.path = filepath.Join(filepath.Dir(j.path), "missing", "bad")
	if err := os.WriteFile(filepath.Dir(j.path), []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	before = cloneJournal(j.state)
	if err := j.Update(job.ID, "failed", "explicit_failure", "session-a", ""); err == nil {
		t.Fatal("expected Update disk error")
	}
	if !reflect.DeepEqual(before, j.state) || j.Session(job.Message.Conversation) != "" {
		t.Fatal("Update failed to roll back")
	}
	j.path = original
	if err := j.Update(job.ID, "failed", "explicit_failure", "", ""); err != nil {
		t.Fatal(err)
	}
	j.path = filepath.Join(original, "blocked")
	before = cloneJournal(j.state)
	if err := j.Retry(job.ID); err == nil {
		t.Fatal("expected Retry disk error")
	}
	if !reflect.DeepEqual(before, j.state) {
		t.Fatal("Retry failed to roll back")
	}
}

func TestJournalCrashRecoveryAndReadOnlyStatus(t *testing.T) {
	for _, state := range []string{"running", "sending"} {
		t.Run(state, func(t *testing.T) {
			b := journalBinding(t)
			j := mustJournal(t, b)
			mustAdd(t, j, "a", "b")
			job := mustNext(t, j)
			if state == "sending" {
				if err := j.Update(job.ID, "sending", "", "session-a", ""); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(j.path)
			if err != nil {
				t.Fatal(err)
			}
			status, err := ReadJournalStatus(b)
			if err != nil || status[0].Status != state {
				t.Fatalf("read-only status: %+v %v", status, err)
			}
			after, _ := os.ReadFile(j.path)
			if string(before) != string(after) {
				t.Fatal("status mutated disk")
			}
			recovered := mustJournal(t, b)
			if recovered.Snapshot()[0].Status != "unknown" {
				t.Fatal("interrupted job not unknown")
			}
			if err := recovered.Retry(job.ID); err == nil {
				t.Fatal("unknown retried blindly")
			}
			next := mustNext(t, recovered)
			if next.Message.ID != "b" || next.Message.Content != "private body" {
				t.Fatal("pending message lost on recovery")
			}
			if err := recovered.Reconcile(job.ID, "replied", ""); err == nil {
				t.Fatal("reconciliation missing evidence ID accepted")
			}
			if err := recovered.Reconcile(job.ID, "replied", "reply-proof"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestJournalRejectsCorruptionAndBindingMismatch(t *testing.T) {
	b := journalBinding(t)
	j := mustJournal(t, b)
	for _, raw := range []string{"null", "{}", "{broken"} {
		if err := os.WriteFile(j.path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenJournal(b); err == nil {
			t.Fatalf("accepted corrupt journal %q", raw)
		}
	}
	if err := WriteJSON(j.path, j.state); err != nil {
		t.Fatal(err)
	}
	wrong := b
	wrong.AgentID = "other"
	if _, err := OpenJournal(wrong); err == nil {
		t.Fatal("accepted another agent's journal")
	}
	wrong = b
	wrong.Revision = "new-revision"
	other := mustJournal(t, wrong)
	if other.path == j.path || len(other.Snapshot()) != 0 {
		t.Fatal("binding revisions were mixed")
	}
	if err := os.WriteFile(j.path, []byte(strings.Repeat(" ", journalMaxBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(b); err == nil {
		t.Fatal("accepted oversized journal")
	}
}

func TestJournalCapacityAndCompletedWindow(t *testing.T) {
	j := mustJournal(t, journalBinding(t))
	ids := make([]string, journalMaxPending)
	for i := range ids {
		ids[i] = fmt.Sprint(i)
	}
	mustAdd(t, j, ids...)
	if j.HasCapacity() {
		t.Fatal("capacity should be exhausted")
	}
	if err := j.AddMessages(inboundBatch("overflow")); err == nil {
		t.Fatal("accepted overflow")
	}
	if len(j.Snapshot()) != journalMaxPending {
		t.Fatal("overflow batch changed state")
	}
	job := mustNext(t, j)
	if err := j.Update(job.ID, "failed", "known_failure", "", ""); err != nil {
		t.Fatal(err)
	}
	if j.HasCapacity() {
		t.Fatal("unresolved failure dropped from capacity")
	}
	if err := j.Retry(job.ID); err != nil {
		t.Fatal(err)
	}
	job = mustNext(t, j)
	if err := j.Update(job.ID, "no_reply", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if !j.HasCapacity() {
		t.Fatal("completion failed to free capacity")
	}
	// Seed a full completed window to exercise eviction without 1024 disk writes.
	next := cloneJournal(j.state)
	next.Jobs = nil
	for i := 0; i < journalMaxCompleted; i++ {
		m := &Message{ID: fmt.Sprint(i), Conversation: "done", Sender: "peer", Receiver: "self"}
		next.Jobs = append(next.Jobs, Job{ID: journalID(j.binding.Scope, j.binding.Revision, m.Conversation, m.ID), Kind: "pm_push", Scope: j.binding.Scope, Revision: j.binding.Revision, Message: m, Status: "no_reply"})
	}
	// An old pending row must enter the newest end of the completion window.
	m := &Message{ID: "old-pending", Conversation: "old", Sender: "peer", Receiver: "self"}
	old := Job{ID: journalID(j.binding.Scope, j.binding.Revision, m.Conversation, m.ID), Kind: "pm_push", Scope: j.binding.Scope, Revision: j.binding.Revision, Message: m, Status: "pending"}
	next.Jobs = append([]Job{old}, next.Jobs...)
	if err := j.save(next); err != nil {
		t.Fatal(err)
	}
	active := mustNext(t, j)
	if err := j.Update(active.ID, "no_reply", "", "", ""); err != nil {
		t.Fatal(err)
	}
	snapshot := j.Snapshot()
	if len(snapshot) != journalMaxCompleted || snapshot[len(snapshot)-1].ID != old.ID {
		t.Fatal("new completion was immediately evicted")
	}
	raw, _ := json.Marshal(map[string]any{"messages": []Message{*m}})
	if err := j.AddMessages(raw); err != nil {
		t.Fatal(err)
	}
	if len(j.Snapshot()) != journalMaxCompleted {
		t.Fatal("latest completed message duplicated")
	}
}

func TestJournalSessionsPriorityAndHintCoalescing(t *testing.T) {
	b := journalBinding(t)
	j := mustJournal(t, b)
	if err := j.AddHint("maintenance_due", json.RawMessage(`{"server":"a"}`)); err != nil {
		t.Fatal(err)
	}
	if err := j.AddHint("maintenance_due", json.RawMessage(`{"server":"b"}`)); err != nil {
		t.Fatal(err)
	}
	if err := j.AddHint("control_pending", json.RawMessage(`{"command_ids":["a","b","a"]}`)); err != nil {
		t.Fatal(err)
	}
	mustAdd(t, j, "1", "2")
	if len(j.Snapshot()) != 5 {
		t.Fatalf("expected 5 jobs, got %d", len(j.Snapshot()))
	}
	job := mustNext(t, j)
	if job.Kind != "pm_push" {
		t.Fatal("PM was not prioritized")
	}
	if _, ok, err := j.Next(); ok || err != nil {
		t.Fatal("second job ran concurrently")
	}
	if err := j.Update(job.ID, "sending", "", "session-one", ""); err != nil {
		t.Fatal(err)
	}
	if err := j.Update(job.ID, "replied", "", "", "reply-one"); err != nil {
		t.Fatal(err)
	}
	second := mustNext(t, j)
	if err := j.Update(second.ID, "no_reply", "", "session-two", ""); err != nil {
		t.Fatal(err)
	}
	if j.Session("conv-1") != "session-one" || j.Session("conv-2") != "session-two" || j.Session("unknown") != "" {
		t.Fatal("sessions crossed conversations")
	}
	hint := mustNext(t, j)
	if err := j.AddHint("maintenance_due", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if len(j.Snapshot()) != 5 {
		t.Fatal("in-flight hint not coalesced")
	}
	if err := j.Update(hint.ID, "completed", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := j.AddHint("maintenance_due", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if len(j.Snapshot()) != 6 {
		t.Fatal("completed periodic hint could not recur")
	}
	if err := j.AddHint("control_pending", json.RawMessage(`{"command_ids":["a","b"]}`)); err != nil {
		t.Fatal(err)
	}
	if len(j.Snapshot()) != 6 {
		t.Fatal("control jobs duplicated")
	}
	j = mustJournal(t, b)
	if j.Session("conv-1") != "session-one" || j.Session("conv-2") != "session-two" {
		t.Fatal("sessions not persisted")
	}
	b = journalBinding(t)
	b.Events = []string{"pm_push"}
	disabled := mustJournal(t, b)
	if err := disabled.AddHint("maintenance_due", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if len(disabled.Snapshot()) != 0 {
		t.Fatal("disabled hint accepted")
	}
	if err := disabled.AddHint("future_commission", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unsupported hint accepted")
	}
}

func TestJournalHintBatchAtomicAndPayloadIsolation(t *testing.T) {
	j := mustJournal(t, journalBinding(t))
	if err := j.AddHint("control_pending", json.RawMessage(`{"command_ids":["valid",""]}`)); err == nil {
		t.Fatal("invalid command ID accepted")
	}
	if len(j.Snapshot()) != 0 {
		t.Fatal("partial command batch persisted")
	}
	if err := j.AddHint("control_pending", json.RawMessage(`{"command_ids":["a","b"],"context":"private"}`)); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"a", "b"} {
		job := mustNext(t, j)
		var payload struct {
			IDs []string `json:"command_ids"`
		}
		if err := json.Unmarshal(job.Data, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.IDs) != 1 || payload.IDs[0] != expected {
			t.Fatalf("command job not isolated: %s", job.Data)
		}
		if err := j.Update(job.ID, "completed", "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.AddHint("control_pending", json.RawMessage(`{"command_ids":["a","b"]}`)); err != nil {
		t.Fatal(err)
	}
	if len(j.Snapshot()) != 2 {
		t.Fatal("completed commands were repeated")
	}
	for _, job := range j.Snapshot() {
		if job.Data != nil {
			t.Fatal("status exposed private payload")
		}
	}
}

func TestJournalSizeLimitAndReconcileRollback(t *testing.T) {
	b := journalBinding(t)
	j := mustJournal(t, b)
	raw, _ := json.Marshal(map[string]any{"messages": []Message{{ID: "large", Conversation: "c", Sender: "peer", Receiver: "self", Content: strings.Repeat("x", journalMaxBytes)}}})
	if err := j.AddMessages(raw); err == nil {
		t.Fatal("oversized batch accepted")
	}
	if len(j.Snapshot()) != 0 {
		t.Fatal("oversized batch mutated journal")
	}
	mustAdd(t, j, "a")
	job := mustNext(t, j)
	j = mustJournal(t, b)
	j.path = filepath.Join(j.path, "impossible")
	before := cloneJournal(j.state)
	if err := j.Reconcile(job.ID, "no_reply", ""); err == nil {
		t.Fatal("expected reconcile disk error")
	}
	if !reflect.DeepEqual(before, j.state) {
		t.Fatal("reconcile failed to roll back")
	}
}

func TestJournalCompactsCompletedPayloadsAndKeepsDeduplication(t *testing.T) {
	b := journalBinding(t)
	j := mustJournal(t, b)
	// Legacy completed rows must be compacted before the byte limit is checked.
	legacy := cloneJournal(j.state)
	for i := 0; i < 800; i++ {
		m := &Message{ID: fmt.Sprint(i), Conversation: "c", Sender: "peer", Receiver: "self", Content: strings.Repeat("x", 20<<10)}
		legacy.Jobs = append(legacy.Jobs, Job{ID: journalID(b.Scope, b.Revision, m.Conversation, m.ID), Kind: "pm_push", Scope: b.Scope, Revision: b.Revision, Message: m, Status: "no_reply"})
	}
	if err := WriteJSON(j.path, legacy); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, b)
	newMessage := Message{ID: "fresh", Conversation: "new", Sender: "peer", Receiver: "self", Content: strings.Repeat("y", 64<<10)}
	raw, _ := json.Marshal(map[string]any{"messages": []Message{newMessage}})
	if err := j.AddMessages(raw); err != nil {
		t.Fatalf("completed bodies blocked intake: %v", err)
	}
	if len(j.state.Jobs) != 801 || j.state.Jobs[0].Message.Content != "" {
		t.Fatal("compaction must preserve completed deduplication keys")
	}
	job := mustNext(t, j)
	if job.Message.Content != newMessage.Content {
		t.Fatal("pending payload was lost")
	}
	if err := j.Update(job.ID, "replied", "", "session", "receipt"); err != nil {
		t.Fatal(err)
	}
	if err := j.AddHint("control_pending", json.RawMessage(`{"command_ids":["control"],"context":"private"}`)); err != nil {
		t.Fatal(err)
	}
	hint := mustNext(t, j)
	if err := j.Update(hint.ID, "accepted", "", "", ""); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, b)
	if j.Session("new") != "session" {
		t.Fatal("compaction lost conversation session")
	}
	for _, saved := range j.state.Jobs {
		if saved.Message != nil && saved.Message.Content != "" || len(saved.Data) != 0 {
			t.Fatal("completed payload retained")
		}
		if saved.ID == job.ID && saved.ReplyID != "receipt" {
			t.Fatal("compaction lost reply receipt")
		}
	}
	if err := j.AddMessages(raw); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := j.Next(); ok || err != nil {
		t.Fatalf("compaction caused duplicate execution: %v %v", ok, err)
	}
}

func TestJournalCompactionFailurePreservesMemory(t *testing.T) {
	j := mustJournal(t, journalBinding(t))
	mustAdd(t, j, "legacy")
	job := mustNext(t, j)
	// Simulate a legacy completed payload still held by the in-memory snapshot.
	j.state.Jobs[0].Status = "no_reply"
	j.path = filepath.Join(j.path, "blocked")
	before := j.state.Jobs[0].Message.Content
	if err := j.AddMessages(inboundBatch("new")); err == nil {
		t.Fatal("expected disk failure")
	}
	if j.state.Jobs[0].Message.Content != before || len(j.state.Jobs) != 1 || j.state.Jobs[0].ID != job.ID {
		t.Fatal("failed compaction mutated shared state")
	}
}

func TestJournalReconcileOptionalCrashRequiresExplicitOutcome(t *testing.T) {
	for _, kind := range []string{"control_pending", "profile_review_due", "maintenance_due"} {
		for _, outcome := range []string{"completed", "failed"} {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				b := journalBinding(t)
				j := mustJournal(t, b)
				if err := j.AddHint(kind, json.RawMessage(`{"command_ids":["command"]}`)); err != nil {
					t.Fatal(err)
				}
				job := mustNext(t, j)
				j = mustJournal(t, b)
				if err := j.Retry(job.ID); err == nil {
					t.Fatal("unknown retried without reconciliation")
				}
				if err := j.Reconcile(job.ID, "no_reply", ""); err == nil {
					t.Fatal("optional event accepted PM outcome")
				}
				if err := j.Reconcile(job.ID, outcome, ""); err != nil {
					t.Fatal(err)
				}
				j = mustJournal(t, b)
				if _, ok, err := j.Next(); ok || err != nil {
					t.Fatalf("reconciliation automatically reran work: %v %v", ok, err)
				}
				if err := j.Retry(job.ID); outcome == "failed" {
					if err != nil {
						t.Fatal(err)
					}
					if next := mustNext(t, j); next.ID != job.ID || !json.Valid(next.Data) {
						t.Fatal("explicit retry lost optional event payload")
					}
				} else if err == nil {
					t.Fatal("completed optional event retried")
				}
			})
		}
	}
	j := mustJournal(t, journalBinding(t))
	mustAdd(t, j, "pm")
	job := mustNext(t, j)
	if err := j.Update(job.ID, "unknown", "", "", ""); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"completed", "failed"} {
		if err := j.Reconcile(job.ID, action, ""); err == nil {
			t.Fatal("PM accepted optional event outcome")
		}
	}
}

func TestJournalReconciliationRejectsActiveAndCompletedJobs(t *testing.T) {
	for _, state := range []string{"pending", "running", "sending", "replied", "no_reply"} {
		t.Run(state, func(t *testing.T) {
			j := mustJournal(t, journalBinding(t))
			mustAdd(t, j, "pm")
			id := j.Snapshot()[0].ID
			if state != "pending" {
				mustNext(t, j)
				if state != "running" {
					if err := j.Update(id, state, "", "", "receipt"); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := j.Snapshot()
			if err := j.Reconcile(id, "no_reply", ""); err == nil {
				t.Fatal("reconciliation accepted active or completed work")
			}
			if !reflect.DeepEqual(before, j.Snapshot()) {
				t.Fatal("rejected reconciliation changed journal")
			}
		})
	}
}

func TestJournalRetryPersistsOperatorIntent(t *testing.T) {
	b := journalBinding(t)
	j := mustJournal(t, b)
	if err := j.AddHint("profile_review_due", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	job := mustNext(t, j)
	if job.Code != "" {
		t.Fatal("new periodic job was marked as an operator retry")
	}
	if err := j.Update(job.ID, "needs_user", "business_unconfirmed", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := j.Retry(job.ID); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, b)
	pending := j.Snapshot()[0]
	if pending.Status != "pending" || pending.Code != "operator_retry" {
		t.Fatalf("retry intent not persisted: %+v", pending)
	}
	next := mustNext(t, j)
	if next.ID != job.ID || next.Code != "operator_retry" {
		t.Fatalf("execution lost retry intent: %+v", next)
	}
}
