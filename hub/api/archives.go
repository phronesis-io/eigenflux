package api

import (
	"net/http"
	"strconv"

	"fluxspace.ai/hub/apperr"
	"fluxspace.ai/hub/archive"
	"fluxspace.ai/hub/followup"
	"fluxspace.ai/hub/models"
)

// RegisterArchives installs the S4 replay surface: archive creation, integrity
// verification, the six viewing modes, participant consent, publication and
// same-spec reprises.
func RegisterArchives(mux *http.ServeMux, srv *Server) {
	mux.HandleFunc("POST /hub/sessions/{id}/archive", srv.createArchive)
	mux.HandleFunc("GET /hub/archives", srv.listArchives)
	mux.HandleFunc("GET /hub/archives/{id}", srv.getArchive)
	mux.HandleFunc("GET /hub/archives/{id}/verify", srv.verifyArchive)
	mux.HandleFunc("GET /hub/archives/{id}/views/{mode}", srv.archiveView)
	mux.HandleFunc("POST /hub/archives/{id}/consent", srv.recordConsent)
	mux.HandleFunc("POST /hub/archives/{id}/publish", srv.publishArchive)
	mux.HandleFunc("POST /hub/archives/{id}/reprise", srv.createReprise)
	mux.HandleFunc("GET /hub/archives/{id}/reprises", srv.listReprises)
	mux.HandleFunc("GET /hub/reprises/{id}", srv.getReprise)
	mux.HandleFunc("POST /hub/reprises/{id}/complete", srv.completeReprise)
}

// archives returns the store or fails loudly. An unconfigured archive layer
// must not answer 200 with an empty list, which would read as "no archives".
func (s *Server) archives() (*archive.Store, error) {
	if s.Archives == nil {
		return nil, apperr.New(http.StatusServiceUnavailable, "ARCHIVE_STORE_UNCONFIGURED",
			"archive store is not configured")
	}
	// A failed persistence write is not a local hiccup: the archive log and
	// the snapshot may now disagree. Refusing the whole surface keeps a later
	// read from presenting state that was never durably recorded (ADR-0011).
	if s.Archives.Degraded() {
		return nil, apperr.New(http.StatusServiceUnavailable, "ARCHIVE_STORE_DEGRADED",
			"archive store stopped after a state change could not be recorded")
	}
	return s.Archives, nil
}

// archiveRecord is the metadata response: the archive without its event stream.
type archiveRecord struct {
	Summary     archive.Summary        `json:"summary"`
	Integrity   archive.Integrity      `json:"integrity"`
	Published   bool                   `json:"published"`
	PublishedAt int64                  `json:"published_at,omitempty"`
	Consent     archive.PolicyDecision `json:"consent"`
	Config      archiveSeatSummary     `json:"config"`
}

type archiveSeatSummary struct {
	TotalRounds   int                  `json:"total_rounds"`
	SeatCount     int                  `json:"seat_count"`
	Seats         []archive.SeatConfig `json:"seats"`
	ReplayConsent string               `json:"replay_consent"`
}

func newArchiveRecord(rec archive.Record) archiveRecord {
	a := rec.Archive
	return archiveRecord{
		Summary: archive.Summary{
			ID: a.ID, SessionID: a.SessionID, Outcome: a.Settlement.Outcome,
			Rounds: a.Settlement.RoundsPlayed, EventCount: a.Integrity.EventCount,
			ContentHash: a.ContentHash(), SkeletonHash: a.SkeletonHash(),
			Verified: a.Integrity.Verified, Complete: a.Integrity.Complete,
			CreatedAt: a.CreatedAt, Public: rec.Published,
			ReplayPolicy: a.Config.SocialContract.ReplayConsent,
		},
		Integrity: a.Integrity, Published: rec.Published, PublishedAt: rec.PublishedAt,
		Consent: rec.Decision,
		Config: archiveSeatSummary{
			TotalRounds: a.Config.TotalRounds, SeatCount: len(a.Config.Seats),
			Seats: a.Config.Seats, ReplayConsent: a.Config.SocialContract.ReplayConsent,
		},
	}
}

// createArchive freezes a finished session. Seat enrichment is optional; when
// it is absent the archive reports the fields it could not capture instead of
// filling them in.
func (s *Server) createArchive(w http.ResponseWriter, r *http.Request) {
	store, err := s.archives()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	var body struct {
		Seats     []archive.SeatConfig `json:"seats,omitempty"`
		ArchiveID string               `json:"archive_id,omitempty"`
	}
	if r.ContentLength > 0 {
		if err := decodeBody(r, &body); err != nil {
			writeDomainError(w, err)
			return
		}
	}
	view, err := s.Manager.State(r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	events, _, err := s.Manager.EventsSince(view.SessionID, "", true, 0)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	// Settlement memories and threads come from the same derivation the
	// follow-up pass uses, so the archive cannot disagree with what each owner
	// was told.
	var followUps []followup.Document
	if view.Status == models.SessionFinished {
		if doc, ferr := followup.Build(view, events, s.now()); ferr == nil {
			followUps = append(followUps, doc)
		}
	}
	built, err := archive.Build(view, events, archive.Options{
		ID: body.ArchiveID, Now: s.now(), Seats: body.Seats, FollowUps: followUps,
	})
	if err != nil {
		writeDomainError(w, apperr.New(http.StatusUnprocessableEntity, "ARCHIVE_BUILD_FAILED", "%v", err))
		return
	}
	rec, err := store.Put(built)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newArchiveRecord(rec))
}

func (s *Server) listArchives(w http.ResponseWriter, r *http.Request) {
	store, err := s.archives()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"archives": store.List()})
}

func (s *Server) getArchive(w http.ResponseWriter, r *http.Request) {
	store, err := s.archives()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	rec, err := store.Get(r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newArchiveRecord(rec))
}

func (s *Server) verifyArchive(w http.ResponseWriter, r *http.Request) {
	store, err := s.archives()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	integrity, err := store.Verify(r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"verified": true, "integrity": integrity})
}

// archiveView projects one of the six modes.
func (s *Server) archiveView(w http.ResponseWriter, r *http.Request) {
	store, err := s.archives()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	rec, err := store.Get(r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	q := archive.Query{Mode: r.PathValue("mode"), SeatID: r.URL.Query().Get("seat_id")}
	if raw := r.URL.Query().Get("speed"); raw != "" {
		speed, convErr := strconv.Atoi(raw)
		if convErr != nil {
			writeDomainError(w, apperr.New(http.StatusBadRequest, "INVALID_SPEED", "speed must be an integer: %s", raw))
			return
		}
		q.Speed = speed
	}
	if raw := r.URL.Query().Get("moment_limit"); raw != "" {
		limit, convErr := strconv.Atoi(raw)
		if convErr != nil {
			writeDomainError(w, apperr.New(http.StatusBadRequest, "INVALID_MOMENT_LIMIT", "moment_limit must be an integer: %s", raw))
			return
		}
		q.MomentLimit = limit
	}
	if compareID := r.URL.Query().Get("compare_with"); compareID != "" {
		other, getErr := store.Get(compareID)
		if getErr != nil {
			writeDomainError(w, getErr)
			return
		}
		q.CompareWith = &other.Archive
	}
	view, err := rec.Archive.Project(q)
	if err != nil {
		writeDomainError(w, apperr.New(http.StatusUnprocessableEntity, "VIEW_NOT_AVAILABLE", "%v", err))
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// recordConsent stores one owner's answer. Consent is only ever received, never
// inferred, so the request must name both the companion and its owner.
func (s *Server) recordConsent(w http.ResponseWriter, r *http.Request) {
	store, err := s.archives()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	var body struct {
		CompanionID string `json:"companion_id"`
		OwnerID     string `json:"owner_id"`
		Accepted    bool   `json:"accepted"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeDomainError(w, err)
		return
	}
	rec, err := store.Get(r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	participant := false
	for _, p := range rec.Archive.SigningParticipants() {
		if p == body.CompanionID {
			participant = true
			break
		}
	}
	if !participant {
		writeDomainError(w, apperr.New(http.StatusForbidden, "NOT_A_SIGNING_PARTICIPANT",
			"companion %q has no seat to consent for in archive %s", body.CompanionID, rec.Archive.ID))
		return
	}
	// GrantConsent goes through the store's durable commit path; calling
	// Ledger().Record directly would leave a restarted Hub with a grant it
	// never recorded (ADR-0011).
	if err := store.GrantConsent(rec.Archive.ID, body.CompanionID, body.OwnerID, body.Accepted, s.now()); err != nil {
		// A rejected grant is the caller's mistake; a failed durable write is
		// the Hub's. Conflating them would turn a 500 into a 400.
		if store.Degraded() {
			writeDomainError(w, apperr.New(http.StatusInternalServerError, "ARCHIVE_PERSIST_FAILED", "%v", err))
		} else {
			writeDomainError(w, apperr.New(http.StatusBadRequest, "INVALID_CONSENT", "%v", err))
		}
		return
	}
	decision, err := store.Ledger().Evaluate(rec.Archive)
	if err != nil {
		writeDomainError(w, apperr.New(http.StatusUnprocessableEntity, "CONSENT_POLICY_INVALID", "%v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"grants": store.Ledger().Grants(rec.Archive.ID), "decision": decision,
	})
}

func (s *Server) publishArchive(w http.ResponseWriter, r *http.Request) {
	store, err := s.archives()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	decision, err := store.Publish(r.PathValue("id"), s.now())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, decision)
}

func (s *Server) createReprise(w http.ResponseWriter, r *http.Request) {
	store, err := s.archives()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	reprise, err := store.CreateReprise(r.PathValue("id"), s.now())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, reprise)
}

func (s *Server) listReprises(w http.ResponseWriter, r *http.Request) {
	store, err := s.archives()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reprises": store.ListReprises(r.PathValue("id"))})
}

func (s *Server) getReprise(w http.ResponseWriter, r *http.Request) {
	store, err := s.archives()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	reprise, err := store.GetReprise(r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, reprise)
}

// completeReprise attaches the archive of the replayed match and computes where
// the two runs diverged.
func (s *Server) completeReprise(w http.ResponseWriter, r *http.Request) {
	store, err := s.archives()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	var body struct {
		NewArchiveID string `json:"new_archive_id"`
		NewSessionID string `json:"new_session_id"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeDomainError(w, err)
		return
	}
	reprise, err := store.CompleteReprise(r.PathValue("id"), body.NewArchiveID, body.NewSessionID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, reprise)
}
