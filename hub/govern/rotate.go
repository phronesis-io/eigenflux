package govern

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Audit log rotation.
//
// The active log is always audit.jsonl. When it is rotated it is closed and
// renamed, atomically, to a deterministic shard audit.<zero-padded-seq>.jsonl,
// where <seq> is the AuditEntry.Seq of the shard's FIRST entry (padded to
// shardPadWidth digits). Naming shards by their first sequence number makes the
// order deterministic and derivable from the data itself, and lets the loader
// read them in chain order without a separate index.
//
// Rotation is NOT an authorization change, so no fake audit entry is written
// for it. The hash chain continues across shards because AuditLog.Append keeps
// the whole history in memory and sets the next entry's PrevHash to the previous
// shard's last Hash; the Store only moves files, it never invents entries.
//
// The active file remains the highest shard for load purposes: it is the only
// file where a torn tail (an interrupted append) is repaired by truncation.
// Rotated shards are immutable, closed files; a torn or malformed line in a
// shard is corruption and fails the load loudly.

const shardPadWidth = 12

func shardName(firstSeq int64) string {
	return fmt.Sprintf("audit.%0*d.jsonl", shardPadWidth, firstSeq)
}

// parseShardName extracts the numeric first-seq from a shard filename. It
// deliberately rejects audit.jsonl (the active file) and any non-numeric
// middle segment so stray files such as audit.backup.jsonl are ignored.
func parseShardName(name string) (int64, bool) {
	const prefix, suffix = "audit.", ".jsonl"
	if name == AuditFileName || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return 0, false
	}
	if len(name) <= len(prefix)+len(suffix) {
		return 0, false
	}
	mid := name[len(prefix) : len(name)-len(suffix)]
	for _, r := range mid {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	seq, err := strconv.ParseInt(mid, 10, 64)
	if err != nil {
		return 0, false
	}
	return seq, true
}

// listShards returns existing shard paths ordered by their first sequence
// number (not lexically, so widths beyond the padding still sort correctly).
func (s *Store) listShards() ([]string, error) {
	dirents, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("list governance shards in %s: %w", s.dir, err)
	}
	type found struct {
		seq  int64
		path string
	}
	var shards []found
	for _, de := range dirents {
		if de.IsDir() {
			continue
		}
		seq, ok := parseShardName(de.Name())
		if !ok {
			continue
		}
		shards = append(shards, found{seq: seq, path: filepath.Join(s.dir, de.Name())})
	}
	sort.Slice(shards, func(i, j int) bool { return shards[i].seq < shards[j].seq })
	out := make([]string, 0, len(shards))
	for _, sh := range shards {
		out = append(out, sh.path)
	}
	return out, nil
}

// LoadAudit reads the whole chain: every shard in sequence order, then the
// active audit.jsonl. A missing file is an empty log, not an error. The
// combined entries are what verifyChain and the snapshot seq validation see,
// so the audit_seq in state.json still names one position in one continuous
// chain regardless of how many shards the history is split across.
//
// A torn final line is repaired only in the active file and reported as a
// TornTailError. In a shard it is corruption and fails the load.
func (s *Store) LoadAudit() ([]AuditEntry, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	shards, err := s.listShards()
	if err != nil {
		return nil, err
	}
	var entries []AuditEntry
	for _, shard := range shards {
		part, err := s.readAuditFileLocked(shard, false)
		if err != nil {
			return nil, err
		}
		entries = append(entries, part...)
	}
	active, err := s.readAuditFileLocked(s.auditPath, true)
	if err != nil {
		var torn *TornTailError
		if errors.As(err, &torn) {
			// The surviving entries of the active file still belong to the
			// chain; hand them back alongside the repair notice.
			return append(entries, active...), err
		}
		return nil, err
	}
	return append(entries, active...), nil
}

// readAuditFileLocked parses one audit file. When allowTornTail is true (the
// active file), an incomplete or malformed final line is truncated and reported
// as a TornTailError; elsewhere it is corruption and returns an error. The
// caller holds s.mu.
func (s *Store) readAuditFileLocked(path string, allowTornTail bool) ([]AuditEntry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read audit file %s: %w", path, err)
	}
	defer f.Close()

	var (
		entries  []AuditEntry
		validEnd int64
	)
	reader := bufio.NewReaderSize(f, 64*1024)
	for line := 1; ; line++ {
		raw, readErr := reader.ReadString('\n')
		complete := strings.HasSuffix(raw, "\n")
		trimmed := strings.TrimSpace(raw)

		if trimmed == "" {
			if complete {
				validEnd += int64(len(raw))
			}
			if readErr != nil {
				break
			}
			continue
		}

		var entry AuditEntry
		if err := json.Unmarshal([]byte(trimmed), &entry); err != nil {
			if !complete && allowTornTail {
				return entries, truncateAuditFile(path, validEnd, err)
			}
			return nil, fmt.Errorf("audit file %s line %d is not a valid entry: %w", path, line, err)
		}
		if !complete {
			if allowTornTail {
				return entries, truncateAuditFile(path, validEnd, nil)
			}
			return nil, fmt.Errorf("audit shard %s line %d is an incomplete entry: a rotated shard must be complete", path, line)
		}
		entries = append(entries, entry)
		validEnd += int64(len(raw))

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, fmt.Errorf("read audit file %s: %w", path, readErr)
		}
	}
	return entries, nil
}

// truncateAuditFile cuts an incomplete tail record and reports it as a
// warning-shaped error carrying how much was dropped. Only the active file is
// ever passed here.
func truncateAuditFile(path string, size int64, cause error) error {
	if err := os.Truncate(path, size); err != nil {
		return fmt.Errorf("repair torn audit tail (%v): %w", cause, err)
	}
	return &TornTailError{Bytes: size}
}

// Rotate explicitly rotates the active log regardless of the size threshold.
// It is a no-op when the active file has no complete entries, so an idle
// Rotate() never produces an empty shard.
func (s *Store) Rotate() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rotateLocked()
}

// maybeRotateLocked rotates before the next append when the active file has
// reached the configured threshold. Rotating before writing keeps a rotation
// failure a clean write failure: no audit entry is durably recorded until the
// fresh active file is ready, so Commit fails closed and governance degrades
// without leaving a recorded-but-unapplied decision.
func (s *Store) maybeRotateLocked() error {
	if s.rotateThreshold <= 0 {
		return nil
	}
	fi, err := s.appendf.Stat()
	if err != nil {
		return fmt.Errorf("stat active audit log: %w", err)
	}
	if fi.Size() < s.rotateThreshold {
		return nil
	}
	return s.rotateLocked()
}

// rotateLocked closes the active handle, atomically renames audit.jsonl to a
// shard named after its first entry, and opens a fresh active file. The caller
// holds s.mu. On any error the store is left either usable (rename failed) or
// closed (reopen failed, which reads as a write failure and degrades).
func (s *Store) rotateLocked() error {
	if s.appendf == nil {
		return fmt.Errorf("governance store %s is closed", s.dir)
	}
	firstSeq, has, err := firstSeqOf(s.auditPath)
	if err != nil {
		return err
	}
	if !has {
		return nil
	}
	shardPath := filepath.Join(s.dir, shardName(firstSeq))
	if _, err := os.Stat(shardPath); err == nil {
		return fmt.Errorf("cannot rotate audit log: shard %s already exists", shardPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat rotation target %s: %w", shardPath, err)
	}

	if err := s.appendf.Sync(); err != nil {
		return fmt.Errorf("flush active audit log before rotation: %w", err)
	}
	if err := s.appendf.Close(); err != nil {
		return fmt.Errorf("close active audit log before rotation: %w", err)
	}
	s.appendf = nil

	if err := os.Rename(s.auditPath, shardPath); err != nil {
		// The original active file is still in place; reopen it so the store
		// remains usable, then report the failed rotation as a write failure.
		if reopenErr := s.reopenActiveLocked(); reopenErr != nil {
			return errors.Join(fmt.Errorf("rotate audit log to %s: %w", shardPath, err), reopenErr)
		}
		return fmt.Errorf("rotate audit log to %s: %w", shardPath, err)
	}
	if err := s.reopenActiveLocked(); err != nil {
		return err
	}
	return nil
}

func (s *Store) reopenActiveLocked() error {
	f, err := os.OpenFile(s.auditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("reopen active audit log %s: %w", s.auditPath, err)
	}
	s.appendf = f
	return nil
}

// firstSeqOf reads only the first complete line of a file to learn the Seq of
// its first entry, which becomes the shard name. It reads at most one line, so
// rotating a large active file stays cheap.
func firstSeqOf(path string) (int64, bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read first audit entry of %s: %w", path, err)
	}
	defer f.Close()
	reader := bufio.NewReaderSize(f, 4096)
	for {
		raw, readErr := reader.ReadString('\n')
		trimmed := strings.TrimSpace(raw)
		if trimmed != "" {
			if !strings.HasSuffix(raw, "\n") {
				return 0, false, fmt.Errorf("first audit entry of %s is incomplete", path)
			}
			var entry AuditEntry
			if err := json.Unmarshal([]byte(trimmed), &entry); err != nil {
				return 0, false, fmt.Errorf("decode first audit entry of %s: %w", path, err)
			}
			return entry.Seq, true, nil
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return 0, false, nil
			}
			return 0, false, readErr
		}
	}
}
