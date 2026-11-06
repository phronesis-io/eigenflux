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
	"sync"
)

// Persistence layout. Two files, deliberately different in shape because the
// two kinds of state have different failure modes:
//
//	audit.jsonl  append-only, one AuditEntry per line, fsynced per append
//	state.json   whole snapshot, replaced atomically (temp file + rename)
//
// The audit log is never rewritten, because rewriting it is exactly the
// tampering the hash chain exists to detect. The permission and budget snapshot
// is tiny and changes wholesale, so an atomic replace is cheaper and safer than
// a second log.
const (
	// StateVersion is the on-disk snapshot schema. A file written by a newer
	// Hub is rejected rather than half-understood.
	StateVersion = 1

	AuditFileName = "audit.jsonl"
	StateFileName = "state.json"
)

// BudgetSnapshot is the durable half of BudgetLedger. Spend keys are
// "<companion_id>|<yyyy-mm>" exactly as the ledger uses them, so a restore does
// not have to re-derive the month from a timestamp and cannot shift spend into
// a different month by accident.
type BudgetSnapshot struct {
	Limits map[string]int `json:"limits"`
	Spent  map[string]int `json:"spent"`
}

// State is everything that must survive a restart except the audit chain
// itself.
//
// Approvals are deliberately absent. An approval authorises one high-impact
// action at the moment of execution; carrying it across a restart would let a
// grant issued before the process died authorise an action after it came back.
// Restart therefore drops every outstanding approval, which is the fail-closed
// direction.
type State struct {
	Version     int                `json:"version"`
	AuditSeq    int64              `json:"audit_seq"`
	Permissions []PermissionConfig `json:"permissions"`
	Budget      BudgetSnapshot     `json:"budget"`
}

// Store is the durable governance store. A nil *Store means "in memory only":
// every helper tolerates it so the Hub can run without a data directory.
type Store struct {
	mu        sync.Mutex
	dir       string
	auditPath string
	statePath string
	appendf   *os.File
}

// OpenStore prepares dir for governance state, creating it when missing.
func OpenStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("governance store requires a directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create governance dir %s: %w", dir, err)
	}
	s := &Store{
		dir:       dir,
		auditPath: filepath.Join(dir, AuditFileName),
		statePath: filepath.Join(dir, StateFileName),
	}
	f, err := os.OpenFile(s.auditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit log %s: %w", s.auditPath, err)
	}
	s.appendf = f
	return s, nil
}

// Dir reports where the store writes, for startup logging.
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// Close releases the append handle. The store is unusable afterwards.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appendf == nil {
		return nil
	}
	err := s.appendf.Close()
	s.appendf = nil
	return err
}

// LoadAudit reads the whole chain. A missing file is an empty log, not an
// error: a fresh Hub has no history yet.
//
// A torn final line is repaired rather than reported, because that is the shape
// a crash leaves behind: the append was interrupted mid-record. The repair is a
// truncation of the incomplete tail, which loses at most the one record that
// was never fully written. A malformed line anywhere else is corruption and
// fails loudly — a hole in the middle of a hash chain is not something to skip
// past.
func (s *Store) LoadAudit() ([]AuditEntry, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.auditPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read audit log %s: %w", s.auditPath, err)
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
			if !complete {
				// Interrupted append: drop the tail so the next append starts
				// on a clean line boundary.
				return entries, s.truncateAuditLocked(validEnd, err)
			}
			return nil, fmt.Errorf("audit log %s line %d is not a valid entry: %w", s.auditPath, line, err)
		}
		if !complete {
			return entries, s.truncateAuditLocked(validEnd, nil)
		}
		entries = append(entries, entry)
		validEnd += int64(len(raw))

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, fmt.Errorf("read audit log %s: %w", s.auditPath, readErr)
		}
	}
	return entries, nil
}

// truncateAuditLocked cuts an incomplete tail record and reports it as a
// warning-shaped error carrying how much was dropped, so the caller can log the
// repair without treating it as a failure.
func (s *Store) truncateAuditLocked(size int64, cause error) error {
	if err := os.Truncate(s.auditPath, size); err != nil {
		return fmt.Errorf("repair torn audit tail (%v): %w", cause, err)
	}
	return &TornTailError{Bytes: size}
}

// TornTailError reports that an interrupted append was removed from the end of
// the audit log. It is not a failure: the chain before it is intact.
type TornTailError struct{ Bytes int64 }

func (e *TornTailError) Error() string {
	return fmt.Sprintf("dropped an incomplete audit record left by an interrupted append (log now ends at byte %d)", e.Bytes)
}

// AppendAudit writes one entry and flushes it to the platform before returning.
// The caller records it in memory only after this succeeds, so the in-memory
// log is never ahead of the durable one.
func (s *Store) AppendAudit(entry AuditEntry) error {
	if s == nil {
		return nil
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode audit entry %d: %w", entry.Seq, err)
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appendf == nil {
		return fmt.Errorf("governance store %s is closed", s.dir)
	}
	if _, err := s.appendf.Write(line); err != nil {
		return fmt.Errorf("append audit entry %d: %w", entry.Seq, err)
	}
	if err := s.appendf.Sync(); err != nil {
		return fmt.Errorf("flush audit entry %d: %w", entry.Seq, err)
	}
	return nil
}

// LoadState reads the snapshot. A missing file yields an empty state so a first
// run needs no seeding step.
func (s *Store) LoadState() (State, error) {
	if s == nil {
		return State{Version: StateVersion}, nil
	}
	raw, err := os.ReadFile(s.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return State{Version: StateVersion}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read governance state %s: %w", s.statePath, err)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return State{}, fmt.Errorf("governance state %s is not valid JSON: %w", s.statePath, err)
	}
	if st.Version != StateVersion {
		return State{}, fmt.Errorf("governance state %s has version %d, this Hub understands %d",
			s.statePath, st.Version, StateVersion)
	}
	return st, nil
}

// SaveState replaces the snapshot atomically. A half-written state file would
// be worse than an old one, so the bytes go to a temp file in the same
// directory and are renamed into place only once they are on disk.
func (s *Store) SaveState(st State) error {
	if s == nil {
		return nil
	}
	st.Version = StateVersion

	tmp, err := os.CreateTemp(s.dir, ".state-*.json")
	if err != nil {
		return fmt.Errorf("create state temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(st); err != nil {
		cleanup()
		return fmt.Errorf("encode governance state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("flush governance state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close governance state: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Rename(tmpName, s.statePath); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("replace governance state: %w", err)
	}
	return nil
}

// Components bundles the three controls with the store that backs them, so
// wiring has one constructor instead of five.
type Components struct {
	Registry *Registry
	Budget   *BudgetLedger
	Audit    *AuditLog
	Store    *Store

	// loadNotes records what was repaired or left half-applied at load. They are
	// reported by LoadNote rather than swallowed, because "one audit record was
	// lost" or "a decision was recorded but never applied" is something an
	// operator must know.
	loadNotes []error
}

// NewMemory returns governance components with no durable backing. This is what
// the Hub uses when no data directory is configured; the components behave
// exactly as before ADR-0008.
func NewMemory() *Components {
	return &Components{
		Registry: NewRegistry(),
		Budget:   NewBudgetLedger(),
		Audit:    NewAuditLog(),
	}
}

// Open loads governance state from dir, verifying the audit chain and refusing
// to start on a snapshot that claims to be newer than the log it backs.
//
// Both refusals are the point of the function. A chain that does not verify
// means entries were edited, removed or reordered; a snapshot ahead of the log
// means an authority change was applied without being recorded. Serving either
// one would be a governance system that lies.
func Open(dir string) (*Components, error) {
	if strings.TrimSpace(dir) == "" {
		return NewMemory(), nil
	}
	store, err := OpenStore(dir)
	if err != nil {
		return nil, err
	}
	components, err := Load(store)
	if err != nil {
		store.Close()
		return nil, err
	}
	return components, nil
}

// Load restores components from an already-open store.
func Load(store *Store) (*Components, error) {
	if store == nil {
		return NewMemory(), nil
	}
	audit, err := OpenAuditLog(store)
	if err != nil {
		return nil, err
	}
	state, err := store.LoadState()
	if err != nil {
		return nil, err
	}
	if state.AuditSeq > audit.LastSeq() {
		return nil, fmt.Errorf(
			"governance state in %s references audit entry %d but the log ends at %d: a change was applied without being recorded",
			store.Dir(), state.AuditSeq, audit.LastSeq())
	}
	registry := NewRegistry()
	if err := registry.Restore(state.Permissions); err != nil {
		return nil, fmt.Errorf("restore permissions: %w", err)
	}
	budget := NewBudgetLedger()
	if err := budget.Restore(state.Budget); err != nil {
		return nil, fmt.Errorf("restore budget: %w", err)
	}
	components := &Components{Registry: registry, Budget: budget, Audit: audit, Store: store}
	if note := audit.LoadNote(); note != nil {
		components.loadNotes = append(components.loadNotes, note)
	}
	if state.AuditSeq < audit.LastSeq() {
		// The crash landed between the append and the snapshot. The log has one
		// or more decisions that were never applied to the state; that is the
		// safe direction, and it is reported rather than silently accepted.
		components.loadNotes = append(components.loadNotes, fmt.Errorf(
			"governance state was last written at audit entry %d but the log ends at %d: %d decision(s) were recorded and never applied",
			state.AuditSeq, audit.LastSeq(), audit.LastSeq()-state.AuditSeq))
	}
	return components, nil
}

// LoadNote reports what was repaired or left half-applied at load: a torn audit
// tail, a log that ran ahead of the state snapshot, or both. Nil means a clean
// load.
func (c *Components) LoadNote() error {
	if c == nil {
		return nil
	}
	return errors.Join(c.loadNotes...)
}

// LastSeq reports the sequence number of the most recent recorded decision, so
// the state snapshot can name the log position it corresponds to.
func (c *Components) SnapshotState() State {
	return State{
		Version:     StateVersion,
		AuditSeq:    c.Audit.LastSeq(),
		Permissions: c.Registry.Snapshot(),
		Budget:      c.Budget.Snapshot(),
	}
}

// Commit records one decision durably: the audit entry first, then the state
// snapshot it produced.
//
// The order is not arbitrary. Audit-then-state means a crash can leave a
// recorded decision that was not applied, which the next load detects and
// reports. State-then-audit would leave an applied permission change with no
// record of who made it, which is the failure the audit log exists to prevent.
func (c *Components) Commit(entry AuditEntry) error {
	if _, err := c.Audit.Append(entry); err != nil {
		return err
	}
	return c.Store.SaveState(c.SnapshotState())
}
