package govern

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildShardedComponents produces a deterministic chain split across three
// rotated shards plus the active file, using explicit rotation only:
//
//	shard "audit.000000000001.jsonl"  seq 1..3
//	shard "audit.000000000004.jsonl"  seq 4..6
//	shard "audit.000000000007.jsonl"  seq 7..9
//	active "audit.jsonl"              seq 10..11
//
// The in-memory log keeps the whole history, so the seq + prev_hash chain stays
// continuous across every boundary.
func buildShardedComponents(t *testing.T, dir string) *Components {
	t.Helper()
	c := openComponentsForTest(t, dir, WithRotateThreshold(0))
	batch := func(n int) {
		for i := 0; i < n; i++ {
			commitTestEntry(t, c, "comp_shards")
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
	if err := c.Audit.Verify(); err != nil {
		t.Fatalf("built chain does not verify: %v", err)
	}
	return c
}

// refChain reads the real chain back from disk (shards + active), independently
// of the in-memory log, to compare the inventory against.
func refChain(t *testing.T, s *Store) []AuditEntry {
	t.Helper()
	entries, err := s.LoadAudit()
	if err != nil {
		t.Fatalf("LoadAudit: %v", err)
	}
	return entries
}

// TestShardInventoryReportsChainAcrossShards is the core inventory test: it
// proves order, seq ranges, entry counts, byte sizes, first/last hashes that
// match the real chain, and the active-file flag, over >= 3 shards.
func TestShardInventoryReportsChainAcrossShards(t *testing.T) {
	dir := t.TempDir()
	c := buildShardedComponents(t, dir)
	entries := refChain(t, c.Store)
	const wantTotal = 11
	if len(entries) != wantTotal {
		t.Fatalf("reference chain length = %d, want %d", len(entries), wantTotal)
	}

	inv, err := c.Store.ShardInventory()
	if err != nil {
		t.Fatalf("ShardInventory: %v", err)
	}
	if len(inv) != 4 { // 3 shards + active
		t.Fatalf("inventory has %d entries, want 4: %+v", len(inv), inv)
	}

	wantNames := []string{shardName(1), shardName(4), shardName(7), AuditFileName}
	wantFirst := []int64{1, 4, 7, 10}
	wantLast := []int64{3, 6, 9, 11}
	wantCount := []int{3, 3, 3, 2}

	var totalEntries int
	var totalBytes int64
	activeSeen := 0
	for i, info := range inv {
		if info.Name != wantNames[i] {
			t.Errorf("inventory[%d].Name = %q, want %q (sequence order)", i, info.Name, wantNames[i])
		}
		if wantPath := filepath.Join(c.Store.Dir(), wantNames[i]); info.Path != wantPath {
			t.Errorf("inventory[%d].Path = %q, want %q", i, info.Path, wantPath)
		}
		if info.FirstSeq != wantFirst[i] || info.LastSeq != wantLast[i] {
			t.Errorf("inventory[%d] seq range = %d..%d, want %d..%d",
				i, info.FirstSeq, info.LastSeq, wantFirst[i], wantLast[i])
		}
		if info.Entries != wantCount[i] {
			t.Errorf("inventory[%d].Entries = %d, want %d", i, info.Entries, wantCount[i])
		}

		// Byte size must be the real on-disk size of the file it describes.
		fi, err := os.Stat(info.Path)
		if err != nil {
			t.Fatalf("stat %s: %v", info.Path, err)
		}
		if info.Bytes != fi.Size() {
			t.Errorf("inventory[%d].Bytes = %d, want real size %d", i, info.Bytes, fi.Size())
		}

		// First/last hashes must be the actual Hash fields from the real chain.
		if got := entries[info.FirstSeq-1].Hash; info.FirstHash != got {
			t.Errorf("inventory[%d].FirstHash = %q, want chain hash %q", i, info.FirstHash, got)
		}
		if got := entries[info.LastSeq-1].Hash; info.LastHash != got {
			t.Errorf("inventory[%d].LastHash = %q, want chain hash %q", i, info.LastHash, got)
		}

		// Chain continuity: the last hash of a file is exactly the prev_hash of
		// the next file's first entry. This is what keeps the chain verifiable
		// across shards, and proves the reported hashes are real chain links.
		if i+1 < len(inv) {
			nextFirst := entries[inv[i+1].FirstSeq-1]
			if nextFirst.PrevHash != info.LastHash {
				t.Errorf("chain break at boundary before seq %d: prev_hash %q != previous last_hash %q",
					inv[i+1].FirstSeq, nextFirst.PrevHash, info.LastHash)
			}
		}

		if info.Active {
			activeSeen++
			if i != len(inv)-1 {
				t.Errorf("active file is not the last inventory entry (index %d)", i)
			}
			if info.Name != AuditFileName {
				t.Errorf("active file name = %q, want %q", info.Name, AuditFileName)
			}
		}
		totalEntries += info.Entries
		totalBytes += info.Bytes
	}
	if activeSeen != 1 {
		t.Fatalf("exactly one file must be marked active, got %d", activeSeen)
	}
	if totalEntries != wantTotal {
		t.Errorf("sum of entry counts = %d, want chain length %d", totalEntries, wantTotal)
	}

	// Sum of byte sizes is exactly what ExportAudit will stream.
	var exported bytes.Buffer
	if err := c.Store.ExportAudit(&exported); err != nil {
		t.Fatalf("ExportAudit: %v", err)
	}
	if int64(exported.Len()) != totalBytes {
		t.Errorf("exported bytes = %d, want sum of shard bytes %d", exported.Len(), totalBytes)
	}
}

// TestShardInventoryZeroShards covers the three "nothing baked yet" cases:
// a fresh durable store with an empty active file, a memory-only (nil) store,
// and a bare store whose active file is absent. None should error, and the
// active file is always represented (Active=true) with zero values.
func TestShardInventoryZeroShards(t *testing.T) {
	t.Run("fresh store with empty active file", func(t *testing.T) {
		dir := t.TempDir()
		c := openComponentsForTest(t, dir, WithRotateThreshold(0))
		inv, err := c.Store.ShardInventory()
		if err != nil {
			t.Fatalf("ShardInventory: %v", err)
		}
		if len(inv) != 1 {
			t.Fatalf("inventory = %+v, want just the active file", inv)
		}
		assertEmptyActive(t, inv[0])
	})

	t.Run("memory-only nil store", func(t *testing.T) {
		var nilStore *Store
		inv, err := nilStore.ShardInventory()
		if err != nil {
			t.Fatalf("ShardInventory on nil store: %v", err)
		}
		if len(inv) != 0 {
			t.Fatalf("nil store inventory = %+v, want empty", inv)
		}
	})

	t.Run("absent active file", func(t *testing.T) {
		dir := t.TempDir()
		// A bare Store with no files on disk at all: no shards, no active file.
		bare := &Store{dir: dir, auditPath: filepath.Join(dir, AuditFileName)}
		inv, err := bare.ShardInventory()
		if err != nil {
			t.Fatalf("ShardInventory with absent active file: %v", err)
		}
		if len(inv) != 1 {
			t.Fatalf("inventory = %+v, want the active slot reported", inv)
		}
		assertEmptyActive(t, inv[0])
	})
}

func assertEmptyActive(t *testing.T, info ShardInfo) {
	t.Helper()
	if !info.Active {
		t.Errorf("active flag = false, want true")
	}
	if info.Name != AuditFileName {
		t.Errorf("name = %q, want %q", info.Name, AuditFileName)
	}
	if info.FirstSeq != 0 || info.LastSeq != 0 || info.Entries != 0 || info.Bytes != 0 {
		t.Errorf("empty active file reported non-zero numbers: %+v", info)
	}
	if info.FirstHash != "" || info.LastHash != "" {
		t.Errorf("empty active file reported hashes: %+v", info)
	}
}

// fileSnapshot is the read-only fingerprint used to prove the audit files are
// untouched by ShardInventory / ExportAudit.
type fileSnapshot struct {
	content []byte
	size    int64
	modTime time.Time
}

func snapshotDir(t *testing.T, dir string) map[string]fileSnapshot {
	t.Helper()
	dirents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	snap := make(map[string]fileSnapshot)
	for _, de := range dirents {
		if de.IsDir() {
			continue
		}
		path := filepath.Join(dir, de.Name())
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		snap[de.Name()] = fileSnapshot{content: content, size: fi.Size(), modTime: fi.ModTime()}
	}
	return snap
}

func assertSnapshotsEqual(t *testing.T, before, after map[string]fileSnapshot) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("file set changed: %d files before, %d after", len(before), len(after))
	}
	for name, b := range before {
		a, ok := after[name]
		if !ok {
			t.Fatalf("file %s was removed or renamed", name)
		}
		if !bytes.Equal(b.content, a.content) {
			t.Errorf("content of %s changed", name)
		}
		if b.size != a.size {
			t.Errorf("size of %s changed: %d -> %d", name, b.size, a.size)
		}
		if !b.modTime.Equal(a.modTime) {
			t.Errorf("mtime of %s changed: %v -> %v", name, b.modTime, a.modTime)
		}
	}
}

// TestShardInventoryAndExportAreReadOnly is the decisive proof: content, size
// and mtime of every file in the governance directory are identical before and
// after both the inventory and the export calls. No file is created, renamed,
// truncated or deleted.
func TestShardInventoryAndExportAreReadOnly(t *testing.T) {
	dir := t.TempDir()
	c := buildShardedComponents(t, dir)

	storeDir := c.Store.Dir()
	before := snapshotDir(t, storeDir)
	if _, err := c.Store.ShardInventory(); err != nil {
		t.Fatalf("ShardInventory: %v", err)
	}
	var buf bytes.Buffer
	if err := c.Store.ExportAudit(&buf); err != nil {
		t.Fatalf("ExportAudit: %v", err)
	}
	if err := c.Store.ExportAuditShard(io.Discard, 4); err != nil {
		t.Fatalf("ExportAuditShard: %v", err)
	}
	after := snapshotDir(t, storeDir)

	assertSnapshotsEqual(t, before, after)
}

// sourceFilesInChainOrder returns the raw bytes of every shard (sequence order)
// followed by the active file, concatenated exactly as ExportAudit should emit.
func sourceFilesInChainOrder(t *testing.T, dir string) []byte {
	t.Helper()
	var out []byte
	for _, name := range listShardNames(t, dir) { // zero-padded => lexical == seq order
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out = append(out, b...)
	}
	b, err := os.ReadFile(filepath.Join(dir, AuditFileName))
	if err != nil {
		t.Fatalf("read active file: %v", err)
	}
	out = append(out, b...)
	return out
}

// TestExportAuditMatchesSourceFiles proves ExportAudit emits the original JSONL
// lines byte-for-byte: exported bytes equal the concatenation of the source
// files, the exported line count equals the chain length, and every exported
// line is a valid, correctly-ordered AuditEntry.
func TestExportAuditMatchesSourceFiles(t *testing.T) {
	dir := t.TempDir()
	c := buildShardedComponents(t, dir)
	entries := refChain(t, c.Store)

	var exported bytes.Buffer
	if err := c.Store.ExportAudit(&exported); err != nil {
		t.Fatalf("ExportAudit: %v", err)
	}
	want := sourceFilesInChainOrder(t, c.Store.Dir())
	if !bytes.Equal(exported.Bytes(), want) {
		t.Fatalf("exported bytes differ from the concatenation of source files (%d vs %d bytes)",
			exported.Len(), len(want))
	}

	scanner := bufio.NewScanner(bytes.NewReader(exported.Bytes()))
	lineCount := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("exported line %d is not a valid entry: %v", lineCount+1, err)
		}
		if e.Seq != int64(lineCount+1) {
			t.Fatalf("exported line %d has seq %d, want %d", lineCount+1, e.Seq, lineCount+1)
		}
		lineCount++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan exported bytes: %v", err)
	}
	if lineCount != len(entries) {
		t.Fatalf("exported line count = %d, want chain length %d", lineCount, len(entries))
	}
	if got := bytes.Count(exported.Bytes(), []byte{'\n'}); got != len(entries) {
		t.Fatalf("exported newline count = %d, want chain length %d", got, len(entries))
	}
}

// TestExportAuditShardByFirstSeq proves a single shard can be exported by the
// first sequence number encoded in its name, byte-for-byte, and that an unknown
// firstSeq fails loudly instead of silently exporting nothing.
func TestExportAuditShardByFirstSeq(t *testing.T) {
	dir := t.TempDir()
	c := buildShardedComponents(t, dir)

	for _, tc := range []struct {
		firstSeq int64
		name     string
	}{
		{1, shardName(1)},
		{4, shardName(4)},
		{7, shardName(7)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var exported bytes.Buffer
			if err := c.Store.ExportAuditShard(&exported, tc.firstSeq); err != nil {
				t.Fatalf("ExportAuditShard(%d): %v", tc.firstSeq, err)
			}
			want, err := os.ReadFile(filepath.Join(c.Store.Dir(), tc.name))
			if err != nil {
				t.Fatalf("read source shard: %v", err)
			}
			if !bytes.Equal(exported.Bytes(), want) {
				t.Fatalf("single-shard export != source bytes (%d vs %d)", exported.Len(), len(want))
			}
		})
	}

	t.Run("unknown firstSeq errors", func(t *testing.T) {
		var buf bytes.Buffer
		if err := c.Store.ExportAuditShard(&buf, 999); err == nil {
			t.Fatal("exporting a non-existent shard must fail, not silently succeed")
		}
		if buf.Len() != 0 {
			t.Fatalf("failed single-shard export wrote %d bytes", buf.Len())
		}
	})
}
