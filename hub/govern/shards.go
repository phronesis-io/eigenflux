package govern

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Audit shard inventory and manual export.
//
// This file is deliberately READ-ONLY. Every rotated audit.<seq>.jsonl shard and
// the active audit.jsonl is part of one continuous `seq + prev_hash` chain that
// must stay verifiable from the very first entry, forever, on every startup.
// Deleting, truncating, compressing or rewriting a shard would create a new
// chain root and make the old history unverifiable, so this package offers no
// such operation. ShardInventory only reports what is on disk; ExportAudit only
// copies raw bytes out. An operator who decides to archive old shards offline
// does so with their own tooling, after a human decision (see
// production/epics/flux-theatre/AUDIT_SHARD_INVENTORY.md).

// ShardInfo describes one audit file — a rotated shard or the active file — as
// measured by reading the file itself. It is a value type: callers can hold and
// compare it freely without affecting the store.
//
// FirstHash / LastHash are the Hash of the first / last AuditEntry in the file,
// i.e. real chain values, not a synthesized "anchor". At a shard boundary the
// LastHash of one shard is exactly the PrevHash of the first entry of the next,
// which is what keeps the chain verifiable across files. Empty files report zero
// seq values and empty hashes.
type ShardInfo struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	FirstSeq  int64  `json:"first_seq"`
	LastSeq   int64  `json:"last_seq"`
	Entries   int    `json:"entries"`
	Bytes     int64  `json:"bytes"`
	FirstHash string `json:"first_hash"`
	LastHash  string `json:"last_hash"`
	Active    bool   `json:"active"`
}

// ShardInventory reports every shard in sequence order followed by the active
// audit.jsonl. It parses every line to report real numbers (seq ranges, entry
// counts, first/last hashes) and uses os.Stat for the true byte size.
//
// It is strictly READ-ONLY: no file is created, renamed, truncated or deleted.
// In particular it does NOT use LoadAudit / readAuditFileLocked, which repair a
// torn tail in the active file by truncating it — a write this call must never
// perform. An incomplete final line in the active file is left untouched on
// disk and simply not counted.
//
// A nil store (memory-only governance), an empty or absent active file, and a
// store with zero shards are all handled cleanly: the result is never an error
// for those cases. The active file is always represented as the final element
// (Active=true), even when it is empty or missing, so the inventory has a stable
// shape.
func (s *Store) ShardInventory() ([]ShardInfo, error) {
	out := make([]ShardInfo, 0)
	if s == nil {
		return out, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	shards, err := s.listShards() // caller holds s.mu
	if err != nil {
		return nil, err
	}

	type fileSlot struct {
		path   string
		active bool
	}
	slots := make([]fileSlot, 0, len(shards)+1)
	for _, p := range shards {
		slots = append(slots, fileSlot{path: p})
	}
	// The active file is the highest shard for load purposes and belongs last.
	slots = append(slots, fileSlot{path: s.auditPath, active: true})

	for _, slot := range slots {
		entries, size, err := scanFileReadOnly(slot.path, slot.active)
		if err != nil {
			return nil, err
		}
		info := ShardInfo{
			Name:    filepath.Base(slot.path),
			Path:    slot.path,
			Entries: len(entries),
			Bytes:   size,
			Active:  slot.active,
		}
		if len(entries) > 0 {
			info.FirstSeq = entries[0].Seq
			info.LastSeq = entries[len(entries)-1].Seq
			info.FirstHash = entries[0].Hash
			info.LastHash = entries[len(entries)-1].Hash
		}
		out = append(out, info)
	}
	return out, nil
}

// scanFileReadOnly parses every complete line of one audit file without ever
// modifying it. It returns the parsed entries and the file's real byte size.
//
// A missing file yields (nil, 0, nil). A complete line that is not a valid entry
// is corruption and returns an error. An incomplete final line (no trailing
// newline) is only legal in the active file: it is a torn tail left by an
// interrupted append. Read-only means we leave those bytes exactly where they
// are and simply do not count them; a torn line in an immutable shard is
// corruption and errors. This is the one place the inventory intentionally
// differs from readAuditFileLocked, which would truncate the active tail.
func scanFileReadOnly(path string, active bool) ([]AuditEntry, int64, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("stat audit file %s: %w", path, err)
	}
	size := fi.Size()

	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open audit file %s: %w", path, err)
	}
	defer f.Close()

	var entries []AuditEntry
	reader := bufio.NewReaderSize(f, 64*1024)
	for line := 1; ; line++ {
		raw, readErr := reader.ReadString('\n')
		complete := strings.HasSuffix(raw, "\n")
		trimmed := strings.TrimSpace(raw)

		if trimmed != "" {
			if !complete {
				if !active {
					return nil, 0, fmt.Errorf("audit shard %s line %d is an incomplete entry: a rotated shard must be complete", path, line)
				}
				// Torn tail in the active file: read-only, leave bytes in place.
			} else {
				var entry AuditEntry
				if err := json.Unmarshal([]byte(trimmed), &entry); err != nil {
					return nil, 0, fmt.Errorf("audit file %s line %d is not a valid entry: %w", path, line, err)
				}
				entries = append(entries, entry)
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, 0, fmt.Errorf("read audit file %s: %w", path, readErr)
		}
	}
	return entries, size, nil
}

// ExportAudit streams the entire chain — every shard in sequence order, then
// the active file — to w as the original JSONL lines, byte-for-byte.
//
// It does NOT reconstruct or re-marshal entries (which could change bytes); it
// copies each file's raw bytes directly with io.Copy, so the output is exactly
// the concatenation of the source files and the whole history is never held in
// memory. (Reusing LoadAudit, which reads it all, would be acceptable, but raw
// streaming is both byte-exact and cheaper.)
//
// It is strictly READ-ONLY. A missing active file simply contributes no bytes.
// The caller may write w to any destination (file, pipe, object upload); the
// store's files are never altered.
func (s *Store) ExportAudit(w io.Writer) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	shards, err := s.listShards()
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(shards)+1)
	paths = append(paths, shards...)
	paths = append(paths, s.auditPath)
	for _, p := range paths {
		if err := copyAuditFileTo(w, p); err != nil {
			return err
		}
	}
	return nil
}

// ExportAuditShard streams exactly one rotated shard, identified by the first
// sequence number encoded in its filename, to w as its original bytes. It is
// read-only and intended for archiving a single old shard offline. A nil store
// or an unknown firstSeq returns an error rather than silently exporting
// nothing, so an operator never mistakes a typo for a successful export.
func (s *Store) ExportAuditShard(w io.Writer, firstSeq int64) error {
	if s == nil {
		return fmt.Errorf("export audit shard: governance store is memory-only")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	shards, err := s.listShards()
	if err != nil {
		return err
	}
	for _, p := range shards {
		seq, ok := parseShardName(filepath.Base(p))
		if ok && seq == firstSeq {
			return copyAuditFileTo(w, p)
		}
	}
	return fmt.Errorf("no audit shard starts at seq %d", firstSeq)
}

// copyAuditFileTo copies one file's raw bytes to w. A missing file (only ever
// possible for the active file) is a no-op, not an error.
func copyAuditFileTo(w io.Writer, path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open audit file %s for export: %w", path, err)
	}
	defer f.Close()
	if _, err := io.Copy(w, f); err != nil {
		return fmt.Errorf("export audit file %s: %w", path, err)
	}
	return nil
}
