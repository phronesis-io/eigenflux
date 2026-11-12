package api

import (
	"net/http"

	"fluxspace.ai/hub/apperr"
	"fluxspace.ai/hub/govern"
)

// RegisterCompanions installs the ownership registry (ADR-0009 §2.2).
//
// Binding lives on the Hub rather than the client because ownership is an
// authority claim: a client that asserts "this companion is mine" proves exactly
// as much as a client that asserts "I am the admin".
func RegisterCompanions(mux *http.ServeMux, srv *Server) {
	mux.HandleFunc("POST /hub/companions", srv.bindCompanion)
	mux.HandleFunc("GET /hub/companions/{id}", srv.getCompanion)
}

func (s *Server) bindCompanion(w http.ResponseWriter, r *http.Request) {
	gov, err := s.governance()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	owner := ownerFrom(r)
	if owner == "" {
		// Binding needs an identity to bind to. An anonymous Hub cannot answer
		// "whose companion is this", and inventing an owner id from the request
		// is exactly the assertion this registry exists to replace.
		writeDomainError(w, apperr.New(http.StatusUnauthorized, "AUTH_REQUIRED",
			"binding a companion requires an authenticated owner; this Hub is running unauthenticated"))
		return
	}
	var body struct {
		CompanionID string `json:"companion_id"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeDomainError(w, err)
		return
	}
	if body.CompanionID == "" {
		writeDomainError(w, apperr.New(http.StatusBadRequest, "COMPANION_ID_REQUIRED",
			"companion_id is required"))
		return
	}
	binding, err := gov.Owners.Bind(body.CompanionID, owner, s.now())
	if err != nil {
		writeDomainError(w, apperr.New(http.StatusConflict, "COMPANION_ALREADY_BOUND", "%v", err))
		return
	}
	if err := s.commit(gov, r, govern.AuditEntry{
		OwnerID: owner, CompanionID: body.CompanionID, Operation: "bind_companion",
		Tier: "ownership", Outcome: govern.OutcomeAllowed,
		Detail: "companion bound to its owning account",
	}); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, binding)
}

func (s *Server) getCompanion(w http.ResponseWriter, r *http.Request) {
	gov, err := s.governance()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	companionID := r.PathValue("id")
	if !s.authorizeCompanion(w, r, gov, companionID) {
		return
	}
	binding, ok := gov.Owners.Get(companionID)
	if !ok {
		writeDomainError(w, apperr.New(http.StatusNotFound, "COMPANION_NOT_REGISTERED",
			"companion %q is not bound to an owner", companionID))
		return
	}
	writeJSON(w, http.StatusOK, binding)
}
