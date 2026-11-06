package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"fluxspace.ai/hub/apperr"
	"fluxspace.ai/hub/archive"
	"fluxspace.ai/hub/followup"
	invitationpkg "fluxspace.ai/hub/invitation"
	"fluxspace.ai/hub/models"
	"fluxspace.ai/hub/services"
	"fluxspace.ai/hub/session"
)

// Server is the Hub HTTP surface for phase 1. SpectatorToken gates the
// omniscient stream; an empty value disables that route rather than leaving it
// open, because the stream exposes every game secret (GDD "Hub API").
//
// Port, Invitations and FollowUp are optional. When the gateway is not
// configured the related endpoints return an explicit 5xx instead of quietly
// pretending a PM was delivered (ADR-0001 decision 2).
type Server struct {
	Manager        *session.Manager
	SpectatorToken string
	PollInterval   time.Duration
	Invitations    *invitationpkg.Manager
	Port           services.EigenFluxPort
	FollowUp       *followup.Sender
	Archives       *archive.Store
	Governance     *Governance
	// OwnerTokens maps a bearer token to the owner id it authenticates. Empty
	// means unauthenticated; main refuses to start that way without an explicit
	// HUB_ALLOW_UNAUTHENTICATED=1 (ADR-0009 §2.1).
	OwnerTokens map[string]string
	Clock       func() int64
}

func (s *Server) now() int64 {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now().UnixMilli()
}

// RegisterSessions installs every endpoint in fluxspace-contracts.md §3 plus
// the two phase-1 endpoints for discussion and phase advance.
func RegisterSessions(mux *http.ServeMux, srv *Server) {
	mux.HandleFunc("POST /hub/sessions", srv.createSession)
	mux.HandleFunc("POST /hub/sessions/{id}/join", srv.joinSession)
	mux.HandleFunc("POST /hub/sessions/{id}/actions", srv.submitAction)
	mux.HandleFunc("POST /hub/sessions/{id}/advance", srv.advancePhase)
	mux.HandleFunc("POST /hub/sessions/{id}/discussion", srv.postDiscussion)
	mux.HandleFunc("GET /hub/sessions/{id}", srv.getState)
	mux.HandleFunc("GET /hub/sessions/{id}/snapshot", srv.getSnapshot)
	mux.HandleFunc("GET /hub/sessions/{id}/events", srv.participantEvents)
	mux.HandleFunc("GET /hub/sessions/{id}/spectate", srv.spectatorEvents)
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeDomainError(w http.ResponseWriter, err error) {
	if domain, ok := apperr.AsError(err); ok {
		var out apiError
		out.Error.Code = domain.Code
		out.Error.Message = domain.Message
		writeJSON(w, domain.Status, out)
		return
	}
	var out apiError
	out.Error.Code = "INTERNAL"
	out.Error.Message = err.Error()
	writeJSON(w, http.StatusInternalServerError, out)
}

// maxBodyBytes bounds every JSON request body. Without it a single client can
// make the Hub allocate without limit before any validation runs; 1 MiB is far
// above the largest legitimate payload (a full decision submission) and far
// below anything that threatens the process.
const maxBodyBytes = 1 << 20

func decodeBody(r *http.Request, out any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return &apperr.Error{Status: http.StatusBadRequest, Code: "INVALID_BODY", Message: err.Error()}
	}
	return nil
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req session.CreateRequest
	if err := decodeBody(r, &req); err != nil {
		writeDomainError(w, err)
		return
	}
	// An authenticated caller must be in the match it is creating. Claims for
	// other owners are allowed — the invitation flow assembles all seats before
	// anyone joins — but a caller that is not among them would be reserving
	// other people's match slots with no seat of its own.
	if owner := ownerFrom(r); owner != "" {
		mine := false
		for _, claim := range req.Claims {
			if claim.OwnerID == owner {
				mine = true
				break
			}
		}
		if !mine {
			writeDomainError(w, apperr.New(http.StatusForbidden, "CREATOR_NOT_IN_MATCH",
				"an authenticated caller must claim at least one seat as %q", owner))
			return
		}
	}
	view, err := s.Manager.Create(req)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (s *Server) joinSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SeatID  string `json:"seat_id"`
		OwnerID string `json:"owner_id"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeDomainError(w, err)
		return
	}
	// A caller may only join as itself. The manager also checks the seat's
	// owner, but that check compares two client-supplied strings; this one
	// compares a client-supplied string against a token.
	if owner := ownerFrom(r); owner != "" && body.OwnerID != owner {
		writeDomainError(w, apperr.New(http.StatusForbidden, "OWNER_MISMATCH",
			"this token belongs to %q, not %q", owner, body.OwnerID))
		return
	}
	view, token, err := s.Manager.Join(r.PathValue("id"), body.SeatID, body.OwnerID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	// The token is returned once, to the seat that just joined. It is not part
	// of the session view, because every other seat can read that view.
	writeJSON(w, http.StatusOK, map[string]any{"session": view, "stream_token": token})
}

func (s *Server) submitAction(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SeatID string `json:"seat_id"`
		models.DecisionSubmission
	}
	if err := decodeBody(r, &body); err != nil {
		writeDomainError(w, err)
		return
	}
	view, err := s.Manager.SubmitAction(r.PathValue("id"), body.SeatID, body.DecisionSubmission)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) advancePhase(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phase models.Phase `json:"phase"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeDomainError(w, err)
		return
	}
	view, err := s.Manager.Advance(r.PathValue("id"), body.Phase)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) postDiscussion(w http.ResponseWriter, r *http.Request) {
	var msg models.DiscussionMessage
	if err := decodeBody(r, &msg); err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.Manager.PostDiscussion(r.PathValue("id"), msg); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "recorded"})
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	view, err := s.Manager.State(r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) getSnapshot(w http.ResponseWriter, r *http.Request) {
	view, err := s.Manager.Snapshot(r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		// s.now(), not time.Now(): a test that fixes the clock must see the same
		// instant here as everywhere else, or a snapshot assertion becomes a
		// flaky assertion.
		"session": view, "last_event_id": view.LastEventID, "ts": s.now(),
	})
}

func (s *Server) participantEvents(w http.ResponseWriter, r *http.Request) {
	seatID := r.URL.Query().Get("seat_id")
	if seatID == "" {
		writeDomainError(w, &apperr.Error{Status: http.StatusBadRequest,
			Code: "SEAT_ID_REQUIRED", Message: "participant streams require ?seat_id="})
		return
	}
	view, err := s.Manager.State(r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	found := false
	for _, seat := range view.Seats {
		if seat.SeatID == seatID {
			found = true
			break
		}
	}
	if !found {
		writeDomainError(w, &apperr.Error{Status: http.StatusForbidden,
			Code: "UNKNOWN_SEAT", Message: fmt.Sprintf("seat %q is not a participant", seatID)})
		return
	}
	// The seat token is the credential (ADR-0009 §2.3). ?seat_id= is a hint that
	// must agree with the token; before this check, knowing a session id and a
	// seat id was enough to read that seat's private decisions.
	if err := s.Manager.VerifySeatToken(r.PathValue("id"), seatID, seatToken(r)); err != nil {
		writeDomainError(w, err)
		return
	}
	s.stream(w, r, seatID, false)
}

func (s *Server) spectatorEvents(w http.ResponseWriter, r *http.Request) {
	if s.SpectatorToken == "" {
		writeDomainError(w, &apperr.Error{Status: http.StatusServiceUnavailable,
			Code:    "SPECTATOR_AUTH_UNCONFIGURED",
			Message: "omniscient stream disabled: HUB_SPECTATOR_TOKEN is not configured"})
		return
	}
	// Constant-time compare: this is the only credential the Hub accepts, and a
	// byte-by-byte comparison leaks its prefix to anyone who can time responses.
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Spectator-Token")), []byte(s.SpectatorToken)) != 1 {
		writeDomainError(w, &apperr.Error{Status: http.StatusForbidden,
			Code: "SPECTATOR_AUTH_REQUIRED", Message: "invalid spectator token"})
		return
	}
	s.stream(w, r, "", true)
}

// stream polls the retained event log instead of holding a subscriber channel.
// This is deliberate: a slow or disconnected observer can never drop, reorder
// or block an event, and Last-Event-ID recovery is the same read path as a
// live connection (contracts §3).
func (s *Server) stream(w http.ResponseWriter, r *http.Request, seatID string, omniscient bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeDomainError(w, &apperr.Error{Status: http.StatusInternalServerError,
			Code: "STREAMING_UNSUPPORTED", Message: "ResponseWriter cannot flush"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	last := lastEventID(r)
	interval := s.PollInterval
	if interval <= 0 {
		interval = 25 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	sessionID := r.PathValue("id")

	for {
		events, latest, err := s.Manager.EventsSince(sessionID, seatID, omniscient, last)
		if err != nil {
			_, _ = fmt.Fprintf(w, "event: error\ndata: %q\n\n", err.Error())
			flusher.Flush()
			return
		}
		for _, ev := range events {
			encoded, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.ID, encoded)
			last = ev.ID
		}
		flusher.Flush()

		if last >= latest && latest > 0 {
			view, err := s.Manager.State(sessionID)
			if err == nil && view.Status == models.SessionFinished {
				return
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func lastEventID(r *http.Request) int64 {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("last_event_id")
	}
	if raw == "" {
		return 0
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}

// seatToken reads the seat stream token. A header is preferred because query
// strings end up in logs and browser history; the query form exists for clients
// that cannot set headers on an EventSource request.
func seatToken(r *http.Request) string {
	if token := r.Header.Get("X-Seat-Token"); token != "" {
		return token
	}
	return r.URL.Query().Get("seat_token")
}
