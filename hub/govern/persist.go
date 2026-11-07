package govern

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Persistence layout. Two kinds of state, deliberately different in shape
// because they fail differently:
//
//	audit.jsonl (+ audit.<seq>.jsonl shards)  append-only, fsynced per append,
//	                                          rotated by size or on demand
//	state.json                                whole snapshot, replaced atomically
//
// The audit log is never rewritten in place, because rewriting it is exactly
// the tampering the hash chain exists to detect. The permission and budget
// snapshot is tiny and changes wholesale, so an atomic replace is cheaper and
// safer than a second log. See rotate.go for the shard layout.
const (
	// StateVersion is the on-disk snapshot schema. A file written by a newer
	// Hub is rejected rather than half-understood.
	StateVersion = 1

	AuditFileName = "audit.jsonl"
	StateFileName = "state.json"

	// DefaultRotateThreshold is the byte size at which the active audit file is
	// automatically rotated (8 MiB). It is the default for Open/OpenStore; see
	// WithRotateThreshold to change or disable it.
	DefaultRotateThreshold int64 = 8 << 20
)

// Rotation is a new, additive config knob. Omitting the option keeps the
// documented 8 MiB default, so the existing Open/OpenStore call sites (which
// cannot be changed here) get rotation without edits. Passing a value <= 0
// disables size-based rotation entirely, which reproduces the exact
// pre-rotation ("old") behaviour: a single, ever-growing audit.jsonl. Either
// way, write order, fsync-per-write, and degraded-on-write-failure are
// unchanged, and rotation never writes a fake audit entry.

type storeConfig struct {
	rotateThreshold int64
}

func defaultStoreConfig() storeConfig {
	return storeConfig{rotateThreshold: DefaultRotateThreshold}
}

// StoreOption configures a durable store. It is additive: existing callers of
// OpenStore/Open are unaffected.
type StoreOption func(*storeConfig)

// WithRotateThreshold sets the byte size at which the active audit file is
// rotated before the next append. A value <= 0 disables automatic rotation
// (the old behaviour); the fresh, empty active file is still created. Explicit
// Store.Rotate() always works regardless of this value.
func WithRotateThreshold(bytes int64) StoreOption {
	return func(c *storeConfig) { c.rotateThreshold = bytes }
}

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
	Companions  []CompanionBinding `json:"companions"`
}

// Store is the durable governance store. A nil *Store means "in memory only":
// every helper tolerates it so the Hub can run without a data directory.
type Store struct {
	mu              sync.Mutex
	dir             string
	auditPath       string
	statePath       string
	appendf         *os.File
	rotateThreshold int64
	lock            *processLockHandle
}

// OpenStore prepares dir for governance state with the default config. It is
// kept for backward compatibility; new callers may use OpenStoreWithOptions.
func OpenStore(dir string) (*Store, error) {
	return OpenStoreWithOptions(dir)
}

// OpenStoreWithOptions prepares dir for governance state, acquiring the
// advisory process lock and creating the audit/state files when missing.
// The lock is released on every error path here and by Store.Close on
// shutdown.
func OpenStoreWithOptions(dir string, opts ...StoreOption) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("governance store requires a directory")
	}
	cfg := defaultStoreConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve governance dir %s: %w", dir, err)
	}
	if err := os.MkdirAll(absDir, 0o700); err != nil {
		return nil, fmt.Errorf("create governance dir %s: %w", absDir, err)
	}
	lock, err := acquireProcessLock(absDir)
	if err != nil {
		return nil, err
	}
	s := &Store{
		dir:             absDir,
		auditPath:       filepath.Join(absDir, AuditFileName),
		statePath:       filepath.Join(absDir, StateFileName),
		rotateThreshold: cfg.rotateThreshold,
		lock:            lock,
	}
	f, err := os.OpenFile(s.auditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		_ = lock.release()
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

// Close releases the append handle AND the advisory process lock. The store is
// unusable afterwards; a second Close is a no-op. Releasing the lock here is
// what lets a fresh process (or a restart test) take the directory over.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.appendf != nil {
		err = s.appendf.Close()
		s.appendf = nil
	}
	if s.lock != nil {
		if lerr := s.lock.release(); lerr != nil && err == nil {
			err = lerr
		}
		s.lock = nil
	}
	return err
}

// TornTailError reports that an interrupted append was removed from the end of
// the active audit log. It is not a failure: the chain before it is intact.
type TornTailError struct{ Bytes int64 }

func (e *TornTailError) Error() string {
	return fmt.Sprintf("dropped an incomplete audit record left by an interrupted append (log now ends at byte %d)", e.Bytes)
}

// AppendAudit writes one entry and flushes it to the platform before returning.
// The caller records it in memory only after this succeeds, so the in-memory
// log is never ahead of the durable one. A size-based rotation, when due, is
// performed BEFORE the write so a rotation failure fails closed without a
// recorded-but-unapplied decision.
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
	if err := s.maybeRotateLocked(); err != nil {
		return err
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
	Owners   *Ownership
	Store    *Store

	// loadNotes records what was repaired or left half-applied at load. They are
	// reported by LoadNote rather than swallowed, because "one audit record was
	// lost" or "a decision was recorded but never applied" is something an
	// operator must know.
	loadNotes []error
}

// Close releases the store (and therefore the advisory process lock) held by
// these components. It is safe on memory-only components (Store == nil).
func (c *Components) Close() error {
	if c == nil {
		return nil
	}
	return c.Store.Close()
}

// NewMemory returns governance components with no durable backing. This is what
// the Hub uses when no data directory is configured; the components behave
// exactly as before ADR-0008.
func NewMemory() *Components {
	return &Components{
		Registry: NewRegistry(),
		Budget:   NewBudgetLedger(),
		Audit:    NewAuditLog(),
		Owners:   NewOwnership(),
	}
}

// Open loads governance state from dir with the default config, verifying the
// audit chain and refusing to start on a snapshot that claims to be newer than
// the log it backs. Kept for backward compatibility; see OpenWithOptions.
func Open(dir string) (*Components, error) {
	return OpenWithOptions(dir)
}

// OpenWithOptions is Open with StoreOptions (notably WithRotateThreshold).
//
// Both refusals are the point of the function. A chain that does not verify
// means entries were edited, removed or reordered; a snapshot ahead of the log
// means an authority change was applied without being recorded. Serving either
// one would be a governance system that lies.
func OpenWithOptions(dir string, opts ...StoreOption) (*Components, error) {
	if strings.TrimSpace(dir) == "" {
		return NewMemory(), nil
	}
	store, err := OpenStoreWithOptions(dir, opts...)
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
	owners := NewOwnership()
	if err := owners.Restore(state.Companions); err != nil {
		return nil, fmt.Errorf("restore companion ownership: %w", err)
	}
	components := &Components{Registry: registry, Budget: budget, Audit: audit, Owners: owners, Store: store}
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

// SnapshotState reports the state a snapshot corresponds to: the sequence
// number of the most recent recorded decision and the durable control state.
func (c *Components) SnapshotState() State {
	return State{
		Version:     StateVersion,
		AuditSeq:    c.Audit.LastSeq(),
		Permissions: c.Registry.Snapshot(),
		Budget:      c.Budget.Snapshot(),
		Companions:  c.Owners.Snapshot(),
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
