package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Durable persistence layout, mirroring ADR-0008's model used by govern:
//
//	archive.jsonl   append-only, fsynced per append (one event per state change)
//	state.json      whole snapshot, atomically replaced after every append
//
// Write order matters: the event is appended and fsynced BEFORE the snapshot
// that reflects the change is written. A crash in between leaves a log that is
// ahead of the snapshot, which is the safe direction: the next load reports it
// rather than serving a state change that was never recorded. No record, no
// state change.
const (
	archiveStateVersion = 1
	archiveEventFile    = "archive.jsonl"
	archiveStateFile    = "state.json"

	eventPut             = "put"
	eventPublish         = "publish"
	eventGrantConsent    = "consent_grant"
	eventCreateReprise   = "create_reprise"
	eventCompleteReprise = "complete_reprise"
)

// event is one append-only log line. Seq, Kind, ArchiveID and ContentHash are
// the semantic payload; Hash is the tamper-evidence chain value (see
// eventHash). ContentHash is the archive's own Archive.ContentHash(), never a
// second, independently invented hash of the archive bytes.
type event struct {
	Seq         int64  `json:"seq"`
	Kind        string `json:"kind"`
	ArchiveID   string `json:"archive_id"`
	ContentHash string `json:"content_hash"`
	Hash        string `json:"hash"`
}

// ledgerSnapshot is the consent ledger's durable state. Grants are flattened
// and sorted so a snapshot is deterministic; load rebuilds the nested map.
type ledgerSnapshot struct {
	Grants []Grant `json:"grants"`
}

// persistState is the atomically replaced snapshot. It carries the FULL
// serialisable Store state plus the log position it corresponds to.
type persistState struct {
	Version      int            `json:"version"`
	LastSeq      int64          `json:"last_seq"`
	Records      []Record       `json:"records"`
	Order        []string       `json:"order"`
	Ledger       ledgerSnapshot `json:"ledger"`
	Reprises     []Reprise      `json:"reprises"`
	RepriseOrder []string       `json:"reprise_order"`
}

// persistBackend holds every durable field of a Store. It is nil for a
// memory-only NewStore; all of its fields are guarded by the Store's mu.
type persistBackend struct {
	dir       string
	logPath   string
	statePath string
	appendf   *os.File
	lastSeq   int64
	lastHash  string
	degraded  bool
	loadNote  error
}

// OpenStore opens (creating if needed) a durable archive store under dir and
// loads its state. It refuses to start on a corrupt snapshot, a tampered event,
// broken archive bytes, or a snapshot that is ahead of its log. Memory-only
// stores are constructed with NewStore, whose behaviour is unchanged.
func OpenStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("archive store requires a directory")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve archive dir %s: %w", dir, err)
	}
	if err := os.MkdirAll(absDir, 0o700); err != nil {
		return nil, fmt.Errorf("create archive dir %s: %w", absDir, err)
	}
	p := &persistBackend{
		dir:       absDir,
		logPath:   filepath.Join(absDir, archiveEventFile),
		statePath: filepath.Join(absDir, archiveStateFile),
	}
	s := &Store{
		items:    make(map[string]*Record),
		ledger:   NewLedger(),
		reprises: make(map[string]*Reprise),
		persist:  p,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open archive event log %s: %w", p.logPath, err)
	}
	p.appendf = f
	return s, nil
}

// Dir reports the durable directory, or "" for a memory-only store.
func (s *Store) Dir() string {
	if s.persist == nil {
		return ""
	}
	return s.persist.dir
}

// Degraded reports whether a write failure has put the store into fail-closed
// mode. It is always false for a memory-only store.
func (s *Store) Degraded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.persist == nil {
		return false
	}
	return s.persist.degraded
}

// LoadNote reports what was repaired or left half-applied at load time (a torn
// log tail, a log ahead of the snapshot). Nil means a clean load; it is always
// nil for a memory-only store.
func (s *Store) LoadNote() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.persist == nil {
		return nil
	}
	return s.persist.loadNote
}

// Close releases the append handle. It is safe to call twice; a closed log
// makes the next commit fail closed via Degraded.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.persist == nil || s.persist.appendf == nil {
		return nil
	}
	err := s.persist.appendf.Close()
	s.persist.appendf = nil
	return err
}

// failClosedLocked rejects a mutation once a write has failed. The Store's mu
// must be held by the caller.
func (s *Store) failClosedLocked() error {
	if s.persist != nil && s.persist.degraded {
		return fmt.Errorf("archive store %s is degraded after a write failure: refusing to mutate", s.persist.dir)
	}
	return nil
}

// GrantConsent records one participant's consent decision and commits it. The
// HTTP consent handler must route grants through this method (rather than
// Ledger().Record) for a durable store so the grant is persisted; on a
// memory-only store it behaves exactly like a plain Ledger.Record.
func (s *Store) GrantConsent(archiveID, companionID, ownerID string, accepted bool, at int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failClosedLocked(); err != nil {
		return err
	}
	if err := s.ledger.Record(archiveID, companionID, ownerID, accepted, at); err != nil {
		return err
	}
	hash := ""
	if rec, ok := s.items[archiveID]; ok {
		hash = rec.Archive.ContentHash()
	}
	return s.commit(eventGrantConsent, archiveID, hash)
}

// commit durably records one state change. It is called by every mutating
// Store method AFTER that method has applied the change in memory, with the
// Store's mu held. Write order is append-then-snapshot: the event is appended
// and fsynced before the snapshot that reflects the change is replaced. On any
// write error the store is marked degraded and the error is returned, so the
// caller fails closed and no further mutation is attempted. Memory-only stores
// are a no-op, so NewStore behaviour is unchanged.
func (s *Store) commit(kind, id, hash string) error {
	p := s.persist
	if p == nil {
		return nil
	}
	if p.degraded {
		return fmt.Errorf("archive store %s is degraded: refusing to commit", p.dir)
	}
	if p.appendf == nil {
		return s.degrade(fmt.Errorf("archive event log %s is closed", p.logPath))
	}
	seq := p.lastSeq + 1
	ev := event{Seq: seq, Kind: kind, ArchiveID: id, ContentHash: hash}
	ev.Hash = eventHash(p.lastHash, ev)
	line, err := json.Marshal(ev)
	if err != nil {
		return s.degrade(fmt.Errorf("encode archive event %d: %w", seq, err))
	}
	line = append(line, '\n')
	if _, err := p.appendf.Write(line); err != nil {
		return s.degrade(fmt.Errorf("append archive event %d: %w", seq, err))
	}
	if err := p.appendf.Sync(); err != nil {
		return s.degrade(fmt.Errorf("fsync archive event %d: %w", seq, err))
	}
	p.lastSeq = seq
	p.lastHash = ev.Hash
	if err := s.writeSnapshot(); err != nil {
		return s.degrade(err)
	}
	return nil
}

// degrade marks the store fail-closed and returns the triggering error. The
// Store's mu is held by the caller.
func (s *Store) degrade(err error) error {
	s.persist.degraded = true
	return err
}

// writeSnapshot replaces state.json atomically. It is called with the Store's
// mu held, so the snapshot reflects a single consistent in-memory state and no
// partial snapshot is ever visible.
func (s *Store) writeSnapshot() error {
	p := s.persist
	st := s.snapshotState()
	st.Version = archiveStateVersion
	st.LastSeq = p.lastSeq
	raw, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("encode archive snapshot: %w", err)
	}
	tmp, err := os.CreateTemp(p.dir, ".state-*.json")
	if err != nil {
		return fmt.Errorf("create snapshot temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpName) }
	if _, err := tmp.Write(raw); err != nil {
		cleanup()
		return fmt.Errorf("write snapshot temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("fsync snapshot temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close snapshot temp file: %w", err)
	}
	if err := os.Rename(tmpName, p.statePath); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("replace archive snapshot: %w", err)
	}
	if d, err := os.Open(p.dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// snapshotState builds the serialisable snapshot from the current in-memory
// state. The Store's mu is held; the ledger's own mu is taken to copy grants.
func (s *Store) snapshotState() persistState {
	st := persistState{
		Order:        append([]string(nil), s.order...),
		RepriseOrder: append([]string(nil), s.repriseOrder...),
	}
	st.Records = make([]Record, 0, len(s.order))
	for _, id := range s.order {
		if rec, ok := s.items[id]; ok {
			st.Records = append(st.Records, *rec)
		}
	}
	st.Reprises = make([]Reprise, 0, len(s.repriseOrder))
	for _, id := range s.repriseOrder {
		if r, ok := s.reprises[id]; ok {
			st.Reprises = append(st.Reprises, *r)
		}
	}
	s.ledger.mu.Lock()
	defer s.ledger.mu.Unlock()
	aids := make([]string, 0, len(s.ledger.grants))
	for aid := range s.ledger.grants {
		aids = append(aids, aid)
	}
	sort.Strings(aids)
	for _, aid := range aids {
		cids := make([]string, 0, len(s.ledger.grants[aid]))
		for cid := range s.ledger.grants[aid] {
			cids = append(cids, cid)
		}
		sort.Strings(cids)
		for _, cid := range cids {
			st.Ledger.Grants = append(st.Ledger.Grants, s.ledger.grants[aid][cid])
		}
	}
	return st
}

// eventHash covers every semantic field plus the previous chain value. It is
// the LOG's tamper-evidence hash (govern/audit.go preimage style), distinct
// from archive integrity, which is checked only via Archive.Verify().
func eventHash(prev string, e event) string {
	payload, _ := json.Marshal(struct {
		Prev        string `json:"prev"`
		Seq         int64  `json:"seq"`
		Kind        string `json:"kind"`
		ArchiveID   string `json:"archive_id"`
		ContentHash string `json:"content_hash"`
	}{prev, e.Seq, e.Kind, e.ArchiveID, e.ContentHash})
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func knownEventKind(kind string) bool {
	switch kind {
	case eventPut, eventPublish, eventGrantConsent, eventCreateReprise, eventCompleteReprise:
		return true
	default:
		return false
	}
}

// verifyEvents rejects any log whose entries were edited, removed or
// reordered: sequence must be contiguous and every chain hash must recompute.
func verifyEvents(events []event) error {
	prev := ""
	for i, e := range events {
		if e.Seq != int64(i+1) {
			return fmt.Errorf("archive event %d has seq %d: log was reordered or an entry was removed", i+1, e.Seq)
		}
		if !knownEventKind(e.Kind) {
			return fmt.Errorf("archive event %d has unknown kind %q: tampered event", e.Seq, e.Kind)
		}
		if e.ArchiveID == "" {
			return fmt.Errorf("archive event %d has empty archive_id: tampered event", e.Seq)
		}
		want := eventHash(prev, e)
		if want != e.Hash {
			return fmt.Errorf("archive event %d hash mismatch: recorded %s, recomputed %s: tampered event", e.Seq, e.Hash, want)
		}
		prev = e.Hash
	}
	return nil
}

// readEvents parses the append-only log. A final line without a terminating
// newline is an interrupted (torn) append: it is excluded from events and its
// byte offset (goodLen) is returned so load can truncate it. Any other
// malformed line is a hard error.
func readEvents(path string) (events []event, goodLen int64, torn bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("read archive event log %s: %w", path, err)
	}
	lastNL := bytes.LastIndexByte(data, '\n')
	goodLen = int64(lastNL + 1)
	if len(data) > 0 && lastNL < len(data)-1 {
		torn = true
	}
	for _, line := range bytes.Split(data[:int(goodLen)], []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var e event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, 0, false, fmt.Errorf("malformed archive event line %q: %w", string(line), err)
		}
		events = append(events, e)
	}
	return events, goodLen, torn, nil
}

// readState loads the snapshot, refusing an unknown version. A missing file is
// a fresh, empty store.
func (s *Store) readState() (persistState, error) {
	p := s.persist
	raw, err := os.ReadFile(p.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return persistState{Version: archiveStateVersion}, nil
	}
	if err != nil {
		return persistState{}, fmt.Errorf("read archive snapshot %s: %w", p.statePath, err)
	}
	var st persistState
	if err := json.Unmarshal(raw, &st); err != nil {
		return persistState{}, fmt.Errorf("archive snapshot %s is not valid JSON: %w", p.statePath, err)
	}
	if st.Version != archiveStateVersion {
		return persistState{}, fmt.Errorf("archive snapshot %s has version %d, this Hub understands %d",
			p.statePath, st.Version, archiveStateVersion)
	}
	return st, nil
}

// load restores state from disk and verifies it. It refuses to start on a bad
// snapshot, a tampered event, broken archive bytes, or a snapshot ahead of the
// log. A torn tail is truncated and a log ahead of the snapshot is reported via
// LoadNote. Archive bytes are checked only with Archive.Verify()/ContentHash(),
// never with a second, invented hash scheme.
func (s *Store) load() error {
	p := s.persist
	st, err := s.readState()
	if err != nil {
		return err
	}
	events, goodLen, torn, err := readEvents(p.logPath)
	if err != nil {
		return err
	}
	if err := verifyEvents(events); err != nil {
		return fmt.Errorf("archive event log %s does not verify: %w", p.logPath, err)
	}

	var notes []error
	if torn {
		if err := os.Truncate(p.logPath, goodLen); err != nil {
			return fmt.Errorf("truncate torn archive event log %s: %w", p.logPath, err)
		}
		notes = append(notes, fmt.Errorf("dropped an incomplete trailing archive event line (log truncated to %d bytes)", goodLen))
	}

	n := int64(len(events))
	if st.LastSeq > n {
		return fmt.Errorf("archive snapshot last_seq %d is ahead of the event log which ends at seq %d: a state change was applied without being recorded", st.LastSeq, n)
	}
	if st.LastSeq < n {
		notes = append(notes, fmt.Errorf("archive event log ends at seq %d but the snapshot was written at seq %d: %d event(s) were recorded and never applied", n, st.LastSeq, n-st.LastSeq))
	}

	for i := range st.Records {
		if err := st.Records[i].Archive.Verify(); err != nil {
			return fmt.Errorf("archive %q in snapshot failed integrity check: %w", st.Records[i].Archive.ID, err)
		}
	}

	s.items = make(map[string]*Record, len(st.Records))
	for i := range st.Records {
		s.items[st.Records[i].Archive.ID] = &st.Records[i]
	}
	s.order = append([]string(nil), st.Order...)
	s.ledger = NewLedger()
	for _, g := range st.Ledger.Grants {
		if s.ledger.grants[g.ArchiveID] == nil {
			s.ledger.grants[g.ArchiveID] = make(map[string]Grant)
		}
		s.ledger.grants[g.ArchiveID][g.CompanionID] = g
	}
	s.reprises = make(map[string]*Reprise, len(st.Reprises))
	for i := range st.Reprises {
		s.reprises[st.Reprises[i].ID] = &st.Reprises[i]
	}
	s.repriseOrder = append([]string(nil), st.RepriseOrder...)

	p.lastSeq = n
	if len(events) > 0 {
		p.lastHash = events[len(events)-1].Hash
	}
	if len(notes) > 0 {
		p.loadNote = errors.Join(notes...)
	}
	return nil
}
