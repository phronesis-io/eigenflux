package govern

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openTestComponents opens a durable store in a temp dir and registers cleanup
// so the append handle is released before the directory is removed (Windows
// refuses to delete a directory that still has an open file).
func openTestComponents(t *testing.T, dir string) *Components {
	t.Helper()
	components, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = components.Store.Close() })
	return components
}

func TestGovernanceStateSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	now := nov(t)

	first := openTestComponents(t, dir)
	if _, err := first.Registry.Set("comp_a", TierRestrictedWrite, 1000); err != nil {
		t.Fatalf("Set permissions: %v", err)
	}
	if err := first.Budget.SetLimit("comp_a", 500); err != nil {
		t.Fatalf("SetLimit: %v", err)
	}
	spend := first.Budget.Record("comp_a", 120, now)
	if !spend.Allowed {
		t.Fatalf("120 of 500 should be allowed: %+v", spend)
	}
	if err := first.Commit(AuditEntry{
		CompanionID: "comp_a", Operation: "model_call", Tier: "budget",
		Outcome: OutcomeAllowed, Detail: spend.Reason, At: now.UnixMilli(),
	}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := first.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := openTestComponents(t, dir)
	if note := second.LoadNote(); note != nil {
		t.Fatalf("clean restart reported a load note: %v", note)
	}
	cfg, ok := second.Registry.Get("comp_a")
	if !ok || cfg.Tier != TierRestrictedWrite || cfg.Revoked {
		t.Fatalf("permission not restored: %+v ok=%v", cfg, ok)
	}
	limit, spent, configured := second.Budget.Usage("comp_a", now)
	if !configured || limit != 500 || spent != 120 {
		t.Fatalf("budget not restored: %d/%d configured=%v", spent, limit, configured)
	}
	entries := second.Audit.Entries("")
	if len(entries) != 1 || entries[0].Seq != 1 {
		t.Fatalf("audit not restored: %+v", entries)
	}
	if err := second.Audit.Verify(); err != nil {
		t.Fatalf("restored chain does not verify: %v", err)
	}

	// The chain must continue rather than restart at 1.
	appended, err := second.Audit.Append(AuditEntry{
		CompanionID: "comp_a", Operation: "authorize", Tier: TierReadOnly,
		Outcome: OutcomeDenied, Detail: DenyTierTooLow,
	})
	if err != nil {
		t.Fatalf("append after restart: %v", err)
	}
	if appended.Seq != 2 || appended.PrevHash != entries[0].Hash {
		t.Fatalf("chain did not continue: %+v", appended)
	}
}

func TestGovernanceRestartDropsApprovals(t *testing.T) {
	dir := t.TempDir()

	first := openTestComponents(t, dir)
	if _, err := first.Registry.Set("comp_b", TierHighImpact, 1000); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := first.Commit(AuditEntry{
		CompanionID: "comp_b", Operation: "set_permissions", Tier: TierHighImpact,
		Outcome: OutcomeAllowed, Detail: "tier set", At: 1000,
	}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	approval, err := first.Registry.Approve("comp_b", "delete_files", 60, 1000)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if got := first.Registry.Authorize("comp_b", "delete_files", TierHighImpact, 1000); !got.Allowed {
		t.Fatalf("approval should authorise once: %+v", got)
	}
	_ = first.Store.Close()

	second := openTestComponents(t, dir)
	if approvals := second.Registry.Approvals("comp_b"); len(approvals) != 0 {
		t.Fatalf("approvals must not survive a restart, got %+v", approvals)
	}
	// The old approval id is gone, so a high-impact call needs a fresh grant.
	result := second.Registry.Authorize("comp_b", "delete_files", TierHighImpact, 1000)
	if result.Allowed || result.Code != DenyApprovalNeeded {
		t.Fatalf("stale approval was honoured: %+v (old id %s)", result, approval.ID)
	}
}

func TestGovernanceOpenRepairsTornTailAndSaysSo(t *testing.T) {
	dir := t.TempDir()

	first := openTestComponents(t, dir)
	good, err := first.Audit.Append(AuditEntry{
		CompanionID: "comp_c", Operation: "set_permissions", Tier: TierReadOnly,
		Outcome: OutcomeAllowed, Detail: "ok",
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	_ = first.Store.Close()

	// Simulate a crash in the middle of an append: a half-written JSON record
	// with no trailing newline.
	path := filepath.Join(dir, AuditFileName)
	partial := []byte(`{"seq":2,"companion_id":"comp_c","operat`)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	if _, err := f.Write(partial); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := openTestComponents(t, dir)
	note := second.LoadNote()
	if note == nil {
		t.Fatal("a repaired torn tail must be reported, not swallowed")
	}
	var torn *TornTailError
	if !errors.As(note, &torn) {
		t.Fatalf("load note is not a TornTailError: %v", note)
	}
	entries := second.Audit.Entries("")
	if len(entries) != 1 || entries[0].Hash != good.Hash {
		t.Fatalf("surviving entries = %+v", entries)
	}
	if err := second.Audit.Verify(); err != nil {
		t.Fatalf("chain after repair does not verify: %v", err)
	}
	// The repair must leave a clean line boundary, or the next append would be
	// glued onto the fragment.
	if _, err := second.Audit.Append(AuditEntry{
		CompanionID: "comp_c", Operation: "authorize", Tier: TierReadOnly, Outcome: OutcomeAllowed,
	}); err != nil {
		t.Fatalf("append after repair: %v", err)
	}
	third, err := Load(mustStore(t, dir))
	if err != nil {
		t.Fatalf("reload after repair: %v", err)
	}
	defer third.Store.Close()
	if got := len(third.Audit.Entries("")); got != 2 {
		t.Fatalf("entries after repair + append = %d, want 2", got)
	}
}

func mustStore(t *testing.T, dir string) *Store {
	t.Helper()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestGovernanceOpenRefusesTamperedChain(t *testing.T) {
	dir := t.TempDir()

	first := openTestComponents(t, dir)
	for i := 0; i < 2; i++ {
		if _, err := first.Audit.Append(AuditEntry{
			CompanionID: "comp_d", Operation: "authorize", Tier: TierReadOnly,
			Outcome: OutcomeAllowed, Detail: "ok",
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	_ = first.Store.Close()

	path := filepath.Join(dir, AuditFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	tampered := strings.Replace(string(raw), `"outcome":"allowed"`, `"outcome":"denied"`, 1)
	if tampered == string(raw) {
		t.Fatal("tamper did not change the file")
	}
	if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatalf("write tampered audit: %v", err)
	}

	if _, err := Open(dir); err == nil {
		t.Fatal("Open accepted a chain whose entry was edited")
	} else if !strings.Contains(err.Error(), "does not verify") {
		t.Fatalf("error does not name the chain: %v", err)
	}
}

func TestGovernanceOpenRefusesStateAheadOfLog(t *testing.T) {
	dir := t.TempDir()

	first := openTestComponents(t, dir)
	if _, err := first.Audit.Append(AuditEntry{
		CompanionID: "comp_e", Operation: "set_permissions", Tier: TierReadOnly, Outcome: OutcomeAllowed,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := first.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// A snapshot that claims more recorded decisions than the log holds means a
	// permission change was applied without an audit record. That must not load.
	st, err := (State{
		Version: StateVersion, AuditSeq: 7,
		Permissions: []PermissionConfig{{CompanionID: "comp_e", Tier: TierHighImpact}},
	}).toJSON(t)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, StateFileName), st, 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}

	if _, err := Open(dir); err == nil {
		t.Fatal("Open accepted a snapshot ahead of the audit log")
	} else if !strings.Contains(err.Error(), "without being recorded") {
		t.Fatalf("error does not name the cause: %v", err)
	}
}

func TestGovernanceLoadReportsLogAheadOfState(t *testing.T) {
	dir := t.TempDir()

	// Audit written, snapshot never written: exactly the state a crash between
	// the two leaves behind.
	store := mustStore(t, dir)
	audit, err := OpenAuditLog(store)
	if err != nil {
		t.Fatalf("OpenAuditLog: %v", err)
	}
	if _, err := audit.Append(AuditEntry{
		CompanionID: "comp_f", Operation: "authorize", Tier: TierReadOnly, Outcome: OutcomeAllowed,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	components, err := Load(store)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	note := components.LoadNote()
	if note == nil {
		t.Fatal("a log ahead of the snapshot must be reported")
	}
	if !strings.Contains(note.Error(), "never applied") {
		t.Fatalf("note does not explain the gap: %v", note)
	}
	if got := len(components.Audit.Entries("")); got != 1 {
		t.Fatalf("the recorded decision must still be visible, got %d entries", got)
	}
}

func TestGovernanceCommitFailsClosedWhenStoreIsClosed(t *testing.T) {
	dir := t.TempDir()
	components := openTestComponents(t, dir)
	if _, err := components.Registry.Set("comp_g", TierReadOnly, 1); err != nil {
		t.Fatalf("Set: %v", err)
	}
	before := components.Audit.Len()
	if err := components.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	err := components.Commit(AuditEntry{
		CompanionID: "comp_g", Operation: "authorize", Tier: TierReadOnly, Outcome: OutcomeAllowed,
	})
	if err == nil {
		t.Fatal("Commit succeeded against a closed store")
	}
	// The in-memory log must not run ahead of the durable one.
	if got := components.Audit.Len(); got != before {
		t.Fatalf("audit grew after a failed write: %d -> %d", before, got)
	}
}

func TestGovernanceRestoreRejectsCorruptSnapshots(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Restore([]PermissionConfig{{CompanionID: "comp_h", Tier: "root"}}); err == nil {
		t.Fatal("Restore accepted an unknown tier")
	}
	if err := registry.Restore([]PermissionConfig{{Tier: TierReadOnly}}); err == nil {
		t.Fatal("Restore accepted an entry without a companion id")
	}

	budget := NewBudgetLedger()
	if err := budget.Restore(BudgetSnapshot{Limits: map[string]int{"comp_h": 0}}); err == nil {
		t.Fatal("Restore accepted a zero limit")
	}
	if err := budget.Restore(BudgetSnapshot{Spent: map[string]int{"comp_h|2026-11": -5}}); err == nil {
		t.Fatal("Restore accepted negative spend")
	}
}

func TestGovernanceStateVersionMismatchIsRejected(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	if err := os.WriteFile(filepath.Join(dir, StateFileName),
		[]byte(`{"version":99,"audit_seq":0}`), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if _, err := store.LoadState(); err == nil {
		t.Fatal("LoadState accepted a future schema version")
	} else if !strings.Contains(err.Error(), "version 99") {
		t.Fatalf("error does not name the version: %v", err)
	}
}

// toJSON marshals a state the way the store writes it, so a test can craft a
// snapshot by hand.
func (s State) toJSON(t *testing.T) ([]byte, error) {
	t.Helper()
	return json.Marshal(s)
}
