package govern

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// Audit outcomes. "denied" entries matter as much as "allowed" ones: an audit
// log that only records successes cannot answer "what did this companion try".
const (
	OutcomeAllowed = "allowed"
	OutcomeDenied  = "denied"
	OutcomeFailed  = "failed"
)

// AuditEntry is one recorded tool operation.
type AuditEntry struct {
	Seq         int64  `json:"seq"`
	CompanionID string `json:"companion_id"`
	Operation   string `json:"operation"`
	Tier        string `json:"tier"`
	Outcome     string `json:"outcome"`
	Detail      string `json:"detail,omitempty"`
	At          int64  `json:"at"`
	PrevHash    string `json:"prev_hash"`
	Hash        string `json:"hash"`
}

// AuditLog is an append-only, hash-chained record. Chaining is what makes it
// tamper-evident without a trust anchor: editing, deleting or reordering an
// entry breaks every hash after it.
//
// store is optional. When it is set, every entry is written to disk before it is
// recorded in memory, so a restart replays the same chain and the in-memory log
// is never ahead of the durable one.
type AuditLog struct {
	mu      sync.Mutex
	entries []AuditEntry
	store   *Store

	// loadNote records a repair made while loading — currently only a torn
	// tail left by an interrupted append. It is reported to the operator by
	// Components.LoadNote instead of being swallowed.
	loadNote error
}

// NewAuditLog returns an empty, memory-only log.
func NewAuditLog() *AuditLog { return &AuditLog{} }

// OpenAuditLog loads the chain from store and verifies it before returning.
//
// A chain that does not verify is refused, not repaired: "the first broken
// sequence number" is the only honest statement when entries were edited or
// removed, and continuing to append to a broken chain would launder the
// damage into a valid-looking log.
func OpenAuditLog(store *Store) (*AuditLog, error) {
	log := &AuditLog{store: store}
	entries, err := store.LoadAudit()
	if err != nil {
		// A repaired torn tail is reported with the entries that survived; any
		// other error means the log is unusable.
		var torn *TornTailError
		if !errors.As(err, &torn) {
			return nil, err
		}
		log.loadNote = torn
	}
	if err := verifyChain(entries); err != nil {
		return nil, fmt.Errorf("audit chain in %s does not verify: %w", store.Dir(), err)
	}
	log.entries = entries
	return log, nil
}

// LoadNote reports a repair made while loading the chain, if any.
func (l *AuditLog) LoadNote() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loadNote
}

// Append records one entry and returns it with its chain fields filled in.
func (l *AuditLog) Append(entry AuditEntry) (AuditEntry, error) {
	if entry.CompanionID == "" {
		return AuditEntry{}, fmt.Errorf("audit entry requires a companion_id")
	}
	if entry.Outcome == "" {
		return AuditEntry{}, fmt.Errorf("audit entry requires an outcome")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry.Seq = int64(len(l.entries) + 1)
	entry.PrevHash = ""
	if n := len(l.entries); n > 0 {
		entry.PrevHash = l.entries[n-1].Hash
	}
	hash, err := entry.hash()
	if err != nil {
		return AuditEntry{}, err
	}
	entry.Hash = hash
	if l.store != nil {
		if err := l.store.AppendAudit(entry); err != nil {
			return AuditEntry{}, err
		}
	}
	l.entries = append(l.entries, entry)
	return entry, nil
}

// Entries returns a copy, optionally filtered to one companion. An empty
// companionID means "every entry".
func (l *AuditLog) Entries(companionID string) []AuditEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]AuditEntry, 0, len(l.entries))
	for _, e := range l.entries {
		if companionID == "" || e.CompanionID == companionID {
			out = append(out, e)
		}
	}
	return out
}

// Len reports how many entries were recorded.
func (l *AuditLog) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// LastSeq reports the sequence number of the newest entry, or zero when the log
// is empty. It names the log position a state snapshot corresponds to.
func (l *AuditLog) LastSeq() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n := len(l.entries); n > 0 {
		return l.entries[n-1].Seq
	}
	return 0
}

// Verify recomputes the whole chain. It reports the first broken entry by
// sequence number instead of returning a bare boolean, because "the log is
// wrong" is not actionable.
func (l *AuditLog) Verify() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return verifyChain(l.entries)
}

func verifyChain(entries []AuditEntry) error {
	prev := ""
	for i, e := range entries {
		if e.Seq != int64(i+1) {
			return fmt.Errorf("audit entry %d has seq %d: entries were reordered or removed", i+1, e.Seq)
		}
		if e.PrevHash != prev {
			return fmt.Errorf("audit entry %d prev_hash %q does not match %q", e.Seq, e.PrevHash, prev)
		}
		recomputed, err := e.hash()
		if err != nil {
			return fmt.Errorf("audit entry %d: %w", e.Seq, err)
		}
		if recomputed != e.Hash {
			return fmt.Errorf("audit entry %d hash mismatch: recorded %s, recomputed %s", e.Seq, e.Hash, recomputed)
		}
		prev = e.Hash
	}
	return nil
}

// hash covers every field that describes what happened. Seq and PrevHash are
// included, so moving an entry breaks the chain in two places.
func (e AuditEntry) hash() (string, error) {
	payload, err := json.Marshal(struct {
		Seq         int64  `json:"seq"`
		CompanionID string `json:"companion_id"`
		Operation   string `json:"operation"`
		Tier        string `json:"tier"`
		Outcome     string `json:"outcome"`
		Detail      string `json:"detail"`
		At          int64  `json:"at"`
		PrevHash    string `json:"prev_hash"`
	}{e.Seq, e.CompanionID, e.Operation, e.Tier, e.Outcome, e.Detail, e.At, e.PrevHash})
	if err != nil {
		return "", fmt.Errorf("hash audit entry: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Tamper replaces the log's entries for testing verification. It exists only so
// a test can prove tampering is detected; production code never calls it.
func (l *AuditLog) Tamper(mutate func([]AuditEntry) []AuditEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = mutate(l.entries)
}
