package archive

import (
	"net/http"
	"sort"
	"sync"

	"fluxspace.ai/hub/apperr"
)

// Summary is the list projection of an archive: enough to render a replay
// browser without shipping the event stream.
type Summary struct {
	ID           string `json:"id"`
	SessionID    string `json:"session_id"`
	Outcome      string `json:"outcome"`
	Rounds       int    `json:"rounds_played"`
	EventCount   int    `json:"event_count"`
	ContentHash  string `json:"content_hash"`
	SkeletonHash string `json:"skeleton_hash"`
	Verified     bool   `json:"verified"`
	Complete     bool   `json:"complete"`
	CreatedAt    int64  `json:"created_at"`
	Public       bool   `json:"public"`
	ReplayPolicy string `json:"replay_policy"`
}

// Record is one stored archive plus the mutable state that deliberately lives
// outside it: publication and the last consent evaluation. Keeping these out of
// Archive is what lets publishing leave the content hash untouched.
type Record struct {
	Archive     Archive
	Published   bool
	PublishedAt int64
	Decision    PolicyDecision
}

// Store is the in-memory archive registry. Phase 1 keeps archives in memory
// like the rest of the Hub; the verification path is written so that a disk or
// object-store backend can drop in without changing callers.
type Store struct {
	mu           sync.Mutex
	items        map[string]*Record
	order        []string
	ledger       *Ledger
	reprises     map[string]*Reprise
	repriseOrder []string

	// persist is the durable backing. It is nil for a memory-only NewStore;
	// see persist.go. All of its fields are guarded by mu.
	persist *persistBackend
}

// NewStore returns an empty registry with its own consent ledger.
func NewStore() *Store {
	return &Store{
		items:    make(map[string]*Record),
		ledger:   NewLedger(),
		reprises: make(map[string]*Reprise),
	}
}

// Ledger exposes the consent ledger so the HTTP layer can record grants.
func (s *Store) Ledger() *Ledger { return s.ledger }

// Put stores an archive after verifying it. Re-storing the same id with
// different bytes is rejected: an archive id names exactly one immutable
// capture.
func (s *Store) Put(a Archive) (Record, error) {
	if err := a.Verify(); err != nil {
		return Record{}, apperr.New(http.StatusUnprocessableEntity, "ARCHIVE_INVALID", "%v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failClosedLocked(); err != nil {
		return Record{}, err
	}
	if existing, ok := s.items[a.ID]; ok {
		if existing.Archive.ContentHash() != a.ContentHash() {
			return Record{}, apperr.New(http.StatusConflict, "ARCHIVE_ID_CONFLICT",
				"archive %s already exists with a different content hash", a.ID)
		}
		return *existing, nil
	}
	policy := a.Config.SocialContract.ReplayConsent
	if policy == "" {
		policy = PolicyAll
	}
	rec := &Record{Archive: a, Decision: PolicyDecision{Policy: policy}}
	s.items[a.ID] = rec
	s.order = append(s.order, a.ID)
	if err := s.commit(eventPut, a.ID, a.ContentHash()); err != nil {
		return Record{}, err
	}
	return *rec, nil
}

// Get returns one archive record.
func (s *Store) Get(id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.items[id]
	if !ok {
		return Record{}, apperr.New(http.StatusNotFound, "ARCHIVE_NOT_FOUND", "no archive %s", id)
	}
	return *rec, nil
}

// List returns summaries in insertion order.
func (s *Store) List() []Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Summary, 0, len(s.order))
	for _, id := range s.order {
		rec := s.items[id]
		policy := rec.Archive.Config.SocialContract.ReplayConsent
		if policy == "" {
			policy = PolicyAll
		}
		out = append(out, Summary{
			ID: rec.Archive.ID, SessionID: rec.Archive.SessionID,
			Outcome: rec.Archive.Settlement.Outcome, Rounds: rec.Archive.Settlement.RoundsPlayed,
			EventCount: rec.Archive.Integrity.EventCount, ContentHash: rec.Archive.ContentHash(),
			SkeletonHash: rec.Archive.SkeletonHash(), Verified: rec.Archive.Integrity.Verified,
			Complete: rec.Archive.Integrity.Complete, CreatedAt: rec.Archive.CreatedAt,
			Public: rec.Published, ReplayPolicy: policy,
		})
	}
	return out
}

// Verify re-checks a stored archive and reports the failure instead of
// returning a false all-clear.
func (s *Store) Verify(id string) (Integrity, error) {
	rec, err := s.Get(id)
	if err != nil {
		return Integrity{}, err
	}
	if err := rec.Archive.Verify(); err != nil {
		return rec.Archive.Integrity, apperr.New(http.StatusConflict, "ARCHIVE_CORRUPT", "%v", err)
	}
	return rec.Archive.Integrity, nil
}

// Publish gates a public replay on the participant consent ledger. It never
// forces publication: a denied decision is returned as an error carrying the
// exact missing participants.
func (s *Store) Publish(id string, now int64) (PolicyDecision, error) {
	s.mu.Lock()
	rec, ok := s.items[id]
	s.mu.Unlock()
	if !ok {
		return PolicyDecision{}, apperr.New(http.StatusNotFound, "ARCHIVE_NOT_FOUND", "no archive %s", id)
	}
	if err := rec.Archive.Verify(); err != nil {
		return PolicyDecision{}, apperr.New(http.StatusConflict, "ARCHIVE_CORRUPT", "%v", err)
	}
	decision, err := s.ledger.Evaluate(rec.Archive)
	if err != nil {
		return PolicyDecision{}, apperr.New(http.StatusUnprocessableEntity, "CONSENT_POLICY_INVALID", "%v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failClosedLocked(); err != nil {
		return PolicyDecision{}, err
	}
	rec.Decision = decision
	if !decision.Public {
		return decision, apperr.New(http.StatusForbidden, "REPLAY_CONSENT_INCOMPLETE", "%s", decision.Reason)
	}
	if !rec.Published {
		rec.Published = true
		rec.PublishedAt = now
	}
	if err := s.commit(eventPublish, id, rec.Archive.ContentHash()); err != nil {
		return PolicyDecision{}, err
	}
	return decision, nil
}

// CreateReprise authorises a same-spec re-run. The GDD requires every original
// participant's owner to agree, regardless of the archive's publication policy.
func (s *Store) CreateReprise(archiveID string, now int64) (Reprise, error) {
	rec, err := s.Get(archiveID)
	if err != nil {
		return Reprise{}, err
	}
	if err := rec.Archive.Verify(); err != nil {
		return Reprise{}, apperr.New(http.StatusConflict, "ARCHIVE_CORRUPT", "%v", err)
	}
	decision, err := s.ledger.RequireAllForReprise(rec.Archive)
	if err != nil {
		return Reprise{}, apperr.New(http.StatusUnprocessableEntity, "CONSENT_POLICY_INVALID", "%v", err)
	}
	if !decision.Public {
		return Reprise{}, apperr.New(http.StatusForbidden, "REPRISE_CONSENT_INCOMPLETE", "%s", decision.Reason)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failClosedLocked(); err != nil {
		return Reprise{}, err
	}
	id := "reprise_" + archiveID[len("replay_"):] + "_" + itoa(int64(len(s.repriseOrder)+1))
	r := &Reprise{
		ID: id, ArchiveID: archiveID, Type: RepriseSameSpec,
		Status: RepriseStatusAuthorized, CreatedAt: now, Consent: decision,
	}
	s.reprises[id] = r
	s.repriseOrder = append(s.repriseOrder, id)
	if err := s.commit(eventCreateReprise, archiveID, rec.Archive.ContentHash()); err != nil {
		return Reprise{}, err
	}
	return *r, nil
}

// CompleteReprise attaches the archive of the replayed match and computes the
// divergence point. Both archives must share a skeleton; a mismatch is a client
// error, not something to paper over.
func (s *Store) CompleteReprise(repriseID, newArchiveID, newSessionID string) (Reprise, error) {
	s.mu.Lock()
	r, ok := s.reprises[repriseID]
	s.mu.Unlock()
	if !ok {
		return Reprise{}, apperr.New(http.StatusNotFound, "REPRISE_NOT_FOUND", "no reprise %s", repriseID)
	}
	original, err := s.Get(r.ArchiveID)
	if err != nil {
		return Reprise{}, err
	}
	replayed, err := s.Get(newArchiveID)
	if err != nil {
		return Reprise{}, err
	}
	cmp, err := original.Archive.CompareSameSpec(replayed.Archive)
	if err != nil {
		return Reprise{}, apperr.New(http.StatusUnprocessableEntity, "NOT_A_SAME_SPEC_REPRISE", "%v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failClosedLocked(); err != nil {
		return Reprise{}, err
	}
	r.Status = RepriseStatusCompleted
	r.NewSessionID = newSessionID
	r.Comparison = &cmp
	if err := s.commit(eventCompleteReprise, newArchiveID, replayed.Archive.ContentHash()); err != nil {
		return Reprise{}, err
	}
	return *r, nil
}

// GetReprise returns one reprise record.
func (s *Store) GetReprise(id string) (Reprise, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.reprises[id]
	if !ok {
		return Reprise{}, apperr.New(http.StatusNotFound, "REPRISE_NOT_FOUND", "no reprise %s", id)
	}
	return *r, nil
}

// ListReprises returns the reprises for one archive in creation order.
func (s *Store) ListReprises(archiveID string) []Reprise {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Reprise, 0)
	for _, id := range s.repriseOrder {
		if r := s.reprises[id]; r.ArchiveID == archiveID {
			out = append(out, *r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// itoa avoids pulling strconv in for one call site.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
