package api

import (
	"net/http"

	"fluxspace.ai/hub/apperr"
	invitationpkg "fluxspace.ai/hub/invitation"
	"fluxspace.ai/hub/models"
	"fluxspace.ai/hub/session"
)

// RegisterInvitations installs the negotiation endpoints. Everything that
// reaches EigenFlux goes through Server.Port; with no port configured, send
// and poll return 503 rather than faking a round trip.
func RegisterInvitations(mux *http.ServeMux, srv *Server) {
	mux.HandleFunc("POST /hub/invitations", srv.createInvitation)
	mux.HandleFunc("GET /hub/invitations/{id}", srv.getInvitation)
	mux.HandleFunc("POST /hub/invitations/{id}/send", srv.sendInvitation)
	mux.HandleFunc("POST /hub/invitations/{id}/reply", srv.replyInvitation)
	mux.HandleFunc("POST /hub/invitations/{id}/poll", srv.pollInvitation)
	mux.HandleFunc("POST /hub/invitations/{id}/session", srv.createSessionFromInvitation)
}

func (s *Server) invitations() (*invitationpkg.Manager, error) {
	if s.Invitations == nil {
		return nil, &apperr.Error{Status: http.StatusServiceUnavailable,
			Code: "INVITATIONS_UNCONFIGURED", Message: "invitation manager is not configured on this Hub"}
	}
	return s.Invitations, nil
}

func (s *Server) port() error {
	if s.Port == nil {
		return &apperr.Error{Status: http.StatusServiceUnavailable,
			Code:    "EIGENFLUX_PORT_UNCONFIGURED",
			Message: "HUB_GATEWAY_URL is not configured; no real PM can be sent"}
	}
	return nil
}

func (s *Server) createInvitation(w http.ResponseWriter, r *http.Request) {
	mgr, err := s.invitations()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	var req invitationpkg.Request
	if err := decodeBody(r, &req); err != nil {
		writeDomainError(w, err)
		return
	}
	neg, err := mgr.Create(req)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, neg)
}

func (s *Server) getInvitation(w http.ResponseWriter, r *http.Request) {
	mgr, err := s.invitations()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	neg, err := mgr.Get(r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, neg)
}

// sendInvitation is the outbound half of the real PM round trip.
func (s *Server) sendInvitation(w http.ResponseWriter, r *http.Request) {
	mgr, err := s.invitations()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.port(); err != nil {
		writeDomainError(w, err)
		return
	}
	neg, msgID, err := mgr.Send(r.Context(), s.Port, r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitation": neg, "message_id": msgID})
}

// replyInvitation applies a reply supplied by a local client. Replies that
// arrive through EigenFlux PMs are consumed by pollInvitation instead.
func (s *Server) replyInvitation(w http.ResponseWriter, r *http.Request) {
	mgr, err := s.invitations()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	var body struct {
		MessageID string `json:"message_id"`
		models.InvitationReply
	}
	if err := decodeBody(r, &body); err != nil {
		writeDomainError(w, err)
		return
	}
	neg, err := mgr.ApplyReply(r.PathValue("id"), body.MessageID, body.InvitationReply)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, neg)
}

// pollInvitation is the inbound half of the real PM round trip: fetch, persist,
// parse, apply once per message.
func (s *Server) pollInvitation(w http.ResponseWriter, r *http.Request) {
	mgr, err := s.invitations()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.port(); err != nil {
		writeDomainError(w, err)
		return
	}
	neg, applied, err := mgr.PollReplies(r.Context(), s.Port, r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitation": neg, "applied_replies": applied})
}

// createSessionFromInvitation is the bridge from a confirmed negotiation to a
// real Hub match. It reuses the same session.Manager.Create validation as the
// direct endpoint, so seat uniqueness and one-live-match-per-owner still hold.
func (s *Server) createSessionFromInvitation(w http.ResponseWriter, r *http.Request) {
	mgr, err := s.invitations()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if s.Manager == nil {
		writeDomainError(w, &apperr.Error{Status: http.StatusServiceUnavailable,
			Code: "SESSION_MANAGER_UNCONFIGURED", Message: "session manager is not configured"})
		return
	}
	var body struct {
		Seed     int64              `json:"seed"`
		Backfill []models.SeatClaim `json:"backfill"`
		Pace     string             `json:"pace"`
		Mode     string             `json:"mode"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeDomainError(w, err)
		return
	}
	id := r.PathValue("id")
	inv, claims, err := mgr.BuildClaims(id, body.Backfill)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	view, err := s.Manager.Create(session.CreateRequest{
		Invitation: inv, Claims: claims, Seed: body.Seed,
		Pace: body.Pace, Mode: body.Mode,
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := mgr.MarkSessionCreated(id, view.SessionID); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"session": view, "invitation_id": id})
}
