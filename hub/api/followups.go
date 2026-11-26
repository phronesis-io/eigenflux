package api

import (
	"net/http"

	"fluxspace.ai/hub/apperr"
	"fluxspace.ai/hub/followup"
)

// RegisterFollowUps installs the post-match interaction endpoints. GET
// previews the derived document; POST delivers one PM per real owner.
func RegisterFollowUps(mux *http.ServeMux, srv *Server) {
	mux.HandleFunc("GET /hub/sessions/{id}/followup", srv.getFollowUp)
	mux.HandleFunc("POST /hub/sessions/{id}/followup", srv.sendFollowUp)
}

func (s *Server) buildFollowUp(sessionID string) (followup.Document, error) {
	if s.Manager == nil {
		return followup.Document{}, &apperr.Error{Status: http.StatusServiceUnavailable,
			Code: "SESSION_MANAGER_UNCONFIGURED", Message: "session manager is not configured"}
	}
	view, err := s.Manager.State(sessionID)
	if err != nil {
		return followup.Document{}, err
	}
	events, _, err := s.Manager.EventsSince(sessionID, "", true, 0)
	if err != nil {
		return followup.Document{}, err
	}
	return followup.Build(view, events, s.now())
}

func (s *Server) getFollowUp(w http.ResponseWriter, r *http.Request) {
	doc, err := s.buildFollowUp(r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) sendFollowUp(w http.ResponseWriter, r *http.Request) {
	if s.FollowUp == nil {
		writeDomainError(w, &apperr.Error{Status: http.StatusServiceUnavailable,
			Code:    "FOLLOWUP_UNCONFIGURED",
			Message: "HUB_GATEWAY_URL / HUB_AGENT_ID are not configured; no follow-up PM can be sent"})
		return
	}
	doc, err := s.buildFollowUp(r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	view, err := s.Manager.State(r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	deliveries, err := s.FollowUp.Send(r.Context(), view, doc)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": view.SessionID, "deliveries": deliveries})
}
