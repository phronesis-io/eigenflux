package govern

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func openComponentsForTest(t *testing.T, dir string, opts ...StoreOption) *Components {
	t.Helper()
	c, err := OpenWithOptions(dir, opts...)
	if err != nil {
		t.Fatalf("OpenWithOptions(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func commitTestEntry(t *testing.T, c *Components, companion string) {
	t.Helper()
	if err := c.Commit(AuditEntry{
		CompanionID: companion, Operation: "authorize", Tier: TierReadOnly,
		Outcome: OutcomeAllowed, Detail: "ok", At: 1000,
	}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func listShardNames(t *testing.T, dir string) []string {
	t.Helper()
	dirents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	var names []string
	for _, de := range dirents {
		if _, ok := parseShardName(de.Name()); ok {
			names = append(names, de.Name())
		}
	}
	sort.Strings(names)
	return names
}

func writeStateForTest(t *testing.T, dir string, auditSeq int64) {
	t.Helper()
	b, err := json.Marshal(State{Version: StateVersion, AuditSeq: auditSeq})
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, StateFileName), b, 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
}

func appendTornFragment(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := f.Write([]byte(`{"seq":999,"companion_id":"c","operat`)); err != nil {
		t.Fatalf("write torn fragment: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestRotationChainAcrossShards is the core rotation invariant: three explicit
// rotations produce three shards plus an active file, the loader stitches them
// back into one verified chain, and the deterministic shard names are ordered
// by the first sequence number each shard contains.
func TestRotationChainAcrossShards(t *testing.T) {
	dir := t.TempDir()
	c := openComponentsForTest(t, dir, WithRotateThreshold(0)) // explicit rotation only

	batch := func(n int) {
		for i := 0; i < n; i++ {
			commitTestEntry(t, c, "comp_chain")
		}
	}
	batch(3)
	if err := c.Store.Rotate(); err != nil {
		t.Fatalf("rotate 1: %v", err)
	}
	batch(3)
	if err := c.Store.Rotate(); err != nil {
		t.Fatalf("rotate 2: %v", err)
	}
	batch(3)
	if err := c.Store.Rotate(); err != nil {
		t.Fatalf("rotate 3: %v", err)
	}
	batch(2) // active file

	const wantTotal = 11
	if got := c.Audit.Len(); got != wantTotal {
		t.Fatalf("entries = %d, want %d", got, wantTotal)
	}
	if err := c.Audit.Verify(); err != nil {
		t.Fatalf("in-memory chain does not verify: %v", err)
	}

	// Shard names are deterministic: audit.<zero-padded first-seq>.jsonl.
	names := listShardNames(t, dir)
	want := []string{
		"audit.000000000001.jsonl",
		"audit.000000000004.jsonl",
		"audit.000000000007.jsonl",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("shards = %v, want %v", names, want)
	}
	// Rotation is not an authorization change: no fake shard entry for it, and
	// the active file is still audit.jsonl rather than a shard.
	if _, err := os.Stat(filepath.Join(dir, AuditFileName)); err != nil {
		t.Fatalf("active audit.jsonl missing: %v", err)
	}

	if err := c.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	c2 := openComponentsForTest(t, dir, WithRotateThreshold(0))
	if note := c2.LoadNote(); note != nil {
		t.Fatalf("clean reload reported a load note: %v", note)
	}
	entries := c2.Audit.Entries("")
	if len(entries) != wantTotal {
		t.Fatalf("reloaded entries = %d, want %d", len(entries), wantTotal)
	}
	if err := c2.Audit.Verify(); err != nil {
		t.Fatalf("reloaded chain across shards does not verify: %v", err)
	}
	for i := range entries {
		if entries[i].Seq != int64(i+1) {
			t.Fatalf("entry %d has seq %d (shards out of order)", i, entries[i].Seq)
		}
		// The first entry of each new shard keeps the previous shard's last Hash.
		if i > 0 && entries[i].PrevHash != entries[i-1].Hash {
			t.Fatalf("hash chain breaks at shard boundary before entry %d", i+1)
		}
		if !strings.HasPrefix(entries[i].Hash, "sha256:") {
			t.Fatalf("entry %d has no hash", i+1)
		}
	}
	if got := c2.Audit.LastSeq(); got != wantTotal {
		t.Fatalf("LastSeq = %d, want %d", got, wantTotal)
	}
}

// TestRotationByThreshold proves the configurable byte threshold triggers
// rotation automatically (with a tiny threshold) and the resulting shards
// still load as one verified chain.
func TestRotationByThreshold(t *testing.T) {
	dir := t.TempDir()
	c := openComponentsForTest(t, dir, WithRotateThreshold(1))
	const n = 6
	for i := 0; i < n; i++ {
		commitTestEntry(t, c, "comp_thresh")
	}
	if err := c.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	names := listShardNames(t, dir)
	if len(names) < 3 {
		t.Fatalf("threshold produced %d shards, want >= 3: %v", len(names), names)
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Fatalf("shard names are not in sequence order: %v", names)
		}
	}
	c2 := openComponentsForTest(t, dir, WithRotateThreshold(0))
	if got := c2.Audit.Len(); got != n {
		t.Fatalf("reloaded entries = %d, want %d", got, n)
	}
	if err := c2.Audit.Verify(); err != nil {
		t.Fatalf("chain after threshold rotation: %v", err)
	}
}

// TestSnapshotSeqValidationAcrossShards proves the ADR-0008 snapshot rules
// still fire when the log is split across shards: LastSeq is the combined
// chain position, so a snapshot ahead of the log is refused and a log ahead
// of the snapshot is reported.
func TestSnapshotSeqValidationAcrossShards(t *testing.T) {
	dir := t.TempDir()
	c := openComponentsForTest(t, dir, WithRotateThreshold(0))
	for i := 0; i < 6; i++ {
		commitTestEntry(t, c, "comp_snap")
	}
	if err := c.Store.Rotate(); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	for i := 0; i < 2; i++ {
		commitTestEntry(t, c, "comp_snap")
	}
	if err := c.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	t.Run("snapshot ahead of combined log is refused", func(t *testing.T) {
		writeStateForTest(t, dir, 99)
		if _, err := OpenWithOptions(dir, WithRotateThreshold(0)); err == nil {
			t.Fatal("Open accepted a snapshot claiming an entry the sharded log lacks")
		} else if !strings.Contains(err.Error(), "without being recorded") {
			t.Fatalf("error does not name the cause: %v", err)
		}
	})

	t.Run("log ahead of snapshot is reported", func(t *testing.T) {
		writeStateForTest(t, dir, 3)
		c2 := openComponentsForTest(t, dir, WithRotateThreshold(0))
		defer c2.Close()
		note := c2.LoadNote()
		if note == nil || !strings.Contains(note.Error(), "never applied") {
			t.Fatalf("a sharded log ahead of the snapshot must be reported: %v", note)
		}
		if got := c2.Audit.Len(); got != 8 {
			t.Fatalf("reloaded entries = %d, want 8", got)
		}
	})
}

// TestTornTailOnlyInActiveShard proves the torn-tail repair is applied only to
// the active (highest) shard. A torn tail in a rotated, immutable shard is
// corruption and must be refused, not truncated.
func TestTornTailOnlyInActiveShard(t *testing.T) {
	t.Run("torn tail in active file is repaired", func(t *testing.T) {
		dir := t.TempDir()
		c := openComponentsForTest(t, dir, WithRotateThreshold(0))
		commitTestEntry(t, c, "comp_torn")
		if err := c.Store.Rotate(); err != nil {
			t.Fatalf("rotate: %v", err)
		}
		commitTestEntry(t, c, "comp_torn") // one complete entry in the active file
		if err := c.Store.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}

		appendTornFragment(t, filepath.Join(dir, AuditFileName)) // active only

		c2 := openComponentsForTest(t, dir, WithRotateThreshold(0))
		defer c2.Close()
		var torn *TornTailError
		if !errors.As(c2.LoadNote(), &torn) {
			t.Fatalf("a torn active tail must be reported as TornTailError: %v", c2.LoadNote())
		}
		if err := c2.Audit.Verify(); err != nil {
			t.Fatalf("chain after repair: %v", err)
		}
		if got := c2.Audit.Len(); got != 2 {
			t.Fatalf("entries after repair = %d, want 2 (1 shard + 1 active)", got)
		}
		if names := listShardNames(t, dir); len(names) != 1 {
			t.Fatalf("active repair changed the shards: %v", names)
		}
	})

	t.Run("torn tail in a shard is refused as corruption", func(t *testing.T) {
		dir := t.TempDir()
		c := openComponentsForTest(t, dir, WithRotateThreshold(0))
		commitTestEntry(t, c, "comp_torn")
		if err := c.Store.Rotate(); err != nil {
			t.Fatalf("rotate: %v", err)
		}
		if err := c.Store.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}

		names := listShardNames(t, dir)
		if len(names) != 1 {
			t.Fatalf("expected 1 shard, got %v", names)
		}
		appendTornFragment(t, filepath.Join(dir, names[0])) // corrupt a rotated shard

		if _, err := OpenWithOptions(dir, WithRotateThreshold(0)); err == nil {
			t.Fatal("a torn tail in a rotated shard must be refused, not truncated")
		}
	})
}

// TestRotationDoesNotAlterBudgetOrPermissions proves rotation is purely a file
// operation: no fake audit entry is written and the authority state (and its
// snapshot) is byte-for-byte unchanged, including across a restart.
func TestRotationDoesNotAlterBudgetOrPermissions(t *testing.T) {
	dir := t.TempDir()
	c := openComponentsForTest(t, dir, WithRotateThreshold(0))
	now := time.Now()
	if _, err := c.Registry.Set("comp_state", TierRestrictedWrite, 1000); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := c.Budget.SetLimit("comp_state", 500); err != nil {
		t.Fatalf("SetLimit: %v", err)
	}
	if spend := c.Budget.Record("comp_state", 120, now); !spend.Allowed {
		t.Fatalf("Record: %+v", spend)
	}
	for i := 0; i < 4; i++ {
		commitTestEntry(t, c, "comp_state")
	}

	beforeState, _ := json.Marshal(c.SnapshotState())
	beforeLen := c.Audit.Len()
	if err := c.Audit.Verify(); err != nil {
		t.Fatalf("pre-rotation chain: %v", err)
	}

	if err := c.Store.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	afterState, _ := json.Marshal(c.SnapshotState())
	if !bytes.Equal(beforeState, afterState) {
		t.Fatalf("rotation changed the snapshot state:\n before %s\n after  %s", beforeState, afterState)
	}
	if got := c.Audit.Len(); got != beforeLen {
		t.Fatalf("rotation wrote a fake audit entry: %d -> %d", beforeLen, got)
	}
	if err := c.Audit.Verify(); err != nil {
		t.Fatalf("chain after rotation: %v", err)
	}
	cfg, ok := c.Registry.Get("comp_state")
	if !ok || cfg.Tier != TierRestrictedWrite {
		t.Fatalf("permission changed: %+v ok=%v", cfg, ok)
	}
	limit, spent, configured := c.Budget.Usage("comp_state", now)
	if !configured || limit != 500 || spent != 120 {
		t.Fatalf("budget changed: %d/%d configured=%v", spent, limit, configured)
	}

	if err := c.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	c2 := openComponentsForTest(t, dir, WithRotateThreshold(0))
	cfg2, ok := c2.Registry.Get("comp_state")
	if !ok || cfg2.Tier != TierRestrictedWrite {
		t.Fatalf("permission lost after reload: %+v ok=%v", cfg2, ok)
	}
	l2, s2, cf2 := c2.Budget.Usage("comp_state", now)
	if !cf2 || l2 != 500 || s2 != 120 {
		t.Fatalf("budget lost after reload: %d/%d configured=%v", s2, l2, cf2)
	}
	if got := c2.Audit.Len(); got != beforeLen {
		t.Fatalf("entries after reload = %d, want %d", got, beforeLen)
	}
}

// TestCommitFailsClosedAfterRotation asserts the degraded-on-write-failure
// precondition still holds after a rotation. Closing the store is the same
// simulated write failure the api harness uses to drive GOVERNANCE_PERSIST_FAILED
// -> GOVERNANCE_DEGRADED; Commit must fail closed and the in-memory log must
// not run ahead of the durable one.
func TestCommitFailsClosedAfterRotation(t *testing.T) {
	dir := t.TempDir()
	c := openComponentsForTest(t, dir, WithRotateThreshold(0))
	if _, err := c.Registry.Set("comp_degrade", TierReadOnly, 1); err != nil {
		t.Fatalf("Set: %v", err)
	}
	commitTestEntry(t, c, "comp_degrade")
	if err := c.Store.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	before := c.Audit.Len()

	if err := c.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	err := c.Commit(AuditEntry{
		CompanionID: "comp_degrade", Operation: "authorize", Tier: TierReadOnly, Outcome: OutcomeAllowed,
	})
	if err == nil {
		t.Fatal("Commit succeeded against a closed store after rotation")
	}
	if got := c.Audit.Len(); got != before {
		t.Fatalf("audit grew after a failed write: %d -> %d", before, got)
	}
}
