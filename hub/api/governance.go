package api

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"fluxspace.ai/hub/apperr"
	"fluxspace.ai/hub/govern"
)

// Governance bundles the S5 controls so the HTTP layer has one optional
// dependency instead of three. Nil means the Hub was started without them and
// every stateful governance endpoint answers 503 rather than pretending a
// permission was checked. The model-consistency route is the one exception: it
// reads an archive and calls a pure function, so it needs no governance state
// and keeps working when the three controls are absent.
//
// Components carries the three controls plus the store behind them, so a
// decision and its record are committed together (ADR-0008).
type Governance struct {
	*govern.Components

	mu      sync.Mutex
	failure error
}

// degrade stops all further governance work after a decision could not be
// recorded.
//
// The alternative — keep answering authority questions with no durable record
// of them — is exactly the failure the audit log exists to prevent. Serving a
// 503 until the operator restarts is the honest answer, and a restart reloads
// from disk, so the unrecorded change is discarded rather than kept.
func (g *Governance) degrade(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failure == nil {
		g.failure = err
	}
}

// Degraded reports the failure that stopped governance, if any.
func (g *Governance) Degraded() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.failure
}

// RegisterGovernance installs the S5 surface: per-companion permissions and
// approvals, the token budget with its downgrade path, the audit log, and the
// model-version consistency check.
func RegisterGovernance(mux *http.ServeMux, srv *Server) {
	mux.HandleFunc("GET /hub/companions/{id}/permissions", srv.getPermissions)
	mux.HandleFunc("PUT /hub/companions/{id}/permissions", srv.setPermissions)
	mux.HandleFunc("POST /hub/companions/{id}/permissions/revoke", srv.revokePermissions)
	mux.HandleFunc("POST /hub/companions/{id}/approvals", srv.createApproval)
	mux.HandleFunc("POST /hub/companions/{id}/authorize", srv.authorize)
	mux.HandleFunc("GET /hub/companions/{id}/budget", srv.getBudget)
	mux.HandleFunc("PUT /hub/companions/{id}/budget", srv.setBudget)
	mux.HandleFunc("POST /hub/companions/{id}/budget/usage", srv.recordUsage)
	mux.HandleFunc("GET /hub/audit", srv.listAudit)
	mux.HandleFunc("GET /hub/audit/verify", srv.verifyAudit)
	mux.HandleFunc("GET /hub/archives/{id}/model-consistency", srv.archiveModelConsistency)
}

// nowTime exposes the server clock as a timestamp for the governance packages,
// which account budget per UTC month.
func (s *Server) nowTime() time.Time { return time.UnixMilli(s.now()) }

func (s *Server) governance() (*Governance, error) {
	if s.Governance == nil || s.Governance.Components == nil ||
		s.Governance.Registry == nil || s.Governance.Budget == nil ||
		s.Governance.Audit == nil || s.Governance.Owners == nil {
		return nil, apperr.New(http.StatusServiceUnavailable, "GOVERNANCE_UNCONFIGURED",
			"permissions, budget and audit are not configured on this Hub")
	}
	if err := s.Governance.Degraded(); err != nil {
		return nil, apperr.New(http.StatusServiceUnavailable, "GOVERNANCE_DEGRADED",
			"governance stopped after a decision could not be recorded: %v", err)
	}
	return s.Governance, nil
}

// commit records one governance decision and the state change it produced.
//
// Every authorisation and every budget answer goes through here, so the log
// answers "what was allowed or refused" rather than only "what succeeded". If
// the write fails the process degrades instead of continuing: an authority
// decision that cannot be recorded is not one this Hub is allowed to make.
func (s *Server) commit(gov *Governance, r *http.Request, entry govern.AuditEntry) error {
	entry.At = s.now()
	// The owner comes from the token, never from the body: an audit entry that
	// repeats the client's claim records the forgery, not the actor.
	entry.OwnerID = ownerFrom(r)
	if err := gov.Components.Commit(entry); err != nil {
		gov.degrade(err)
		return apperr.New(http.StatusInternalServerError, "GOVERNANCE_PERSIST_FAILED",
			"decision could not be recorded: %v", err)
	}
	return nil
}

func (s *Server) getPermissions(w http.ResponseWriter, r *http.Request) {
	gov, err := s.governance()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	companionID := r.PathValue("id")
	if !s.authorizeCompanion(w, r, gov, companionID) {
		return
	}
	cfg, ok := gov.Registry.Get(companionID)
	if !ok {
		writeDomainError(w, apperr.New(http.StatusNotFound, "PERMISSIONS_NOT_CONFIGURED",
			"companion %q has no permission configuration", companionID))
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) setPermissions(w http.ResponseWriter, r *http.Request) {
	gov, err := s.governance()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	var body struct {
		Tier string `json:"tier"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeDomainError(w, err)
		return
	}
	companionID := r.PathValue("id")
	if !s.authorizeCompanion(w, r, gov, companionID) {
		return
	}
	cfg, err := gov.Registry.Set(companionID, body.Tier, s.now())
	if err != nil {
		writeDomainError(w, apperr.New(http.StatusBadRequest, "INVALID_PERMISSION_TIER", "%v", err))
		return
	}
	if err := s.commit(gov, r, govern.AuditEntry{
		CompanionID: companionID, Operation: "set_permissions", Tier: body.Tier,
		Outcome: govern.OutcomeAllowed, Detail: "permission tier set to " + body.Tier,
	}); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) revokePermissions(w http.ResponseWriter, r *http.Request) {
	gov, err := s.governance()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	companionID := r.PathValue("id")
	if !s.authorizeCompanion(w, r, gov, companionID) {
		return
	}
	cfg, err := gov.Registry.Revoke(companionID, s.now())
	if err != nil {
		writeDomainError(w, apperr.New(http.StatusNotFound, "PERMISSIONS_NOT_CONFIGURED", "%v", err))
		return
	}
	if err := s.commit(gov, r, govern.AuditEntry{
		CompanionID: companionID, Operation: "revoke_permissions", Tier: cfg.Tier,
		Outcome: govern.OutcomeAllowed, Detail: "permissions revoked; tier retained for audit",
	}); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) createApproval(w http.ResponseWriter, r *http.Request) {
	gov, err := s.governance()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	var body struct {
		Operation  string `json:"operation"`
		TTLSeconds int    `json:"ttl_seconds"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeDomainError(w, err)
		return
	}
	companionID := r.PathValue("id")
	if !s.authorizeCompanion(w, r, gov, companionID) {
		return
	}
	approval, err := gov.Registry.Approve(companionID, body.Operation, body.TTLSeconds, s.now())
	if err != nil {
		writeDomainError(w, apperr.New(http.StatusBadRequest, "INVALID_APPROVAL", "%v", err))
		return
	}
	if err := s.commit(gov, r, govern.AuditEntry{
		CompanionID: companionID, Operation: body.Operation, Tier: govern.TierHighImpact,
		Outcome: govern.OutcomeAllowed, Detail: "single-use approval " + approval.ID + " issued",
	}); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, approval)
}

// authorize is the single gate the client calls before using a tool. The
// decision is always audited, allowed or not.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	gov, err := s.governance()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	var body struct {
		Operation    string `json:"operation"`
		RequiredTier string `json:"required_tier"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeDomainError(w, err)
		return
	}
	companionID := r.PathValue("id")
	if !s.authorizeCompanion(w, r, gov, companionID) {
		return
	}
	result := gov.Registry.Authorize(companionID, body.Operation, body.RequiredTier, s.now())
	outcome := govern.OutcomeDenied
	status := http.StatusForbidden
	if result.Allowed {
		outcome = govern.OutcomeAllowed
		status = http.StatusOK
	}
	if err := s.commit(gov, r, govern.AuditEntry{
		CompanionID: companionID, Operation: body.Operation, Tier: body.RequiredTier,
		Outcome: outcome, Detail: result.Code,
	}); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, status, result)
}

func (s *Server) getBudget(w http.ResponseWriter, r *http.Request) {
	gov, err := s.governance()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	companionID := r.PathValue("id")
	if !s.authorizeCompanion(w, r, gov, companionID) {
		return
	}
	limit, spent, configured := gov.Budget.Usage(companionID, s.nowTime())
	writeJSON(w, http.StatusOK, map[string]any{
		"companion_id": companionID, "month": govern.MonthKey(s.nowTime()),
		"monthly_tokens": limit, "spent_tokens": spent,
		"remaining_tokens": limit - spent, "configured": configured,
	})
}

func (s *Server) setBudget(w http.ResponseWriter, r *http.Request) {
	gov, err := s.governance()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	var body struct {
		MonthlyTokens int `json:"monthly_tokens"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeDomainError(w, err)
		return
	}
	companionID := r.PathValue("id")
	if !s.authorizeCompanion(w, r, gov, companionID) {
		return
	}
	if err := gov.Budget.SetLimit(companionID, body.MonthlyTokens); err != nil {
		writeDomainError(w, apperr.New(http.StatusBadRequest, "INVALID_BUDGET_LIMIT", "%v", err))
		return
	}
	if err := s.commit(gov, r, govern.AuditEntry{
		CompanionID: companionID, Operation: "set_budget", Tier: "budget",
		Outcome: govern.OutcomeAllowed,
		Detail:  "monthly token limit set to " + strconv.Itoa(body.MonthlyTokens),
	}); err != nil {
		writeDomainError(w, err)
		return
	}
	limit, spent, _ := gov.Budget.Usage(companionID, s.nowTime())
	writeJSON(w, http.StatusOK, map[string]any{
		"companion_id": companionID, "monthly_tokens": limit, "spent_tokens": spent,
	})
}

// recordUsage books token spend and returns the budget decision, including the
// downgrade the caller must take when the cap is exceeded.
func (s *Server) recordUsage(w http.ResponseWriter, r *http.Request) {
	gov, err := s.governance()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	var body struct {
		Tokens int `json:"tokens"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeDomainError(w, err)
		return
	}
	companionID := r.PathValue("id")
	if !s.authorizeCompanion(w, r, gov, companionID) {
		return
	}
	decision := gov.Budget.Record(companionID, body.Tokens, s.nowTime())
	outcome := govern.OutcomeAllowed
	status := http.StatusOK
	if !decision.Allowed {
		outcome = govern.OutcomeDenied
		status = http.StatusOK // the answer is a decision, not a transport failure
	}
	if err := s.commit(gov, r, govern.AuditEntry{
		CompanionID: companionID, Operation: "model_call", Tier: "budget",
		Outcome: outcome, Detail: decision.Reason,
	}); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, status, decision)
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	gov, err := s.governance()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	entries := gov.Audit.Entries(r.URL.Query().Get("companion_id"))
	// An authenticated caller sees its own decisions only. The chain is one log,
	// but "show me the audit log" must not become a directory of everyone else's
	// companions.
	if owner := ownerFrom(r); owner != "" {
		scoped := make([]govern.AuditEntry, 0, len(entries))
		for _, entry := range entries {
			if entry.OwnerID == owner {
				scoped = append(scoped, entry)
			}
		}
		entries = scoped
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "count": len(entries)})
}

func (s *Server) verifyAudit(w http.ResponseWriter, r *http.Request) {
	gov, err := s.governance()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := gov.Audit.Verify(); err != nil {
		writeDomainError(w, apperr.New(http.StatusConflict, "AUDIT_CHAIN_BROKEN", "%v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"verified": true, "entries": gov.Audit.Len()})
}

// archiveModelConsistency checks that an archived match declares one model
// version across its real seats. Absent versions are reported as unknown rather
// than passed.
func (s *Server) archiveModelConsistency(w http.ResponseWriter, r *http.Request) {
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
	usages := make([]govern.ModelUsage, 0, len(rec.Archive.Config.Seats))
	for _, seat := range rec.Archive.Config.Seats {
		usages = append(usages, govern.ModelUsage{
			SeatID: seat.SeatID, CompanionID: seat.CompanionID,
			ModelVersion: seat.ModelVersion, Official: seat.Official,
		})
	}
	writeJSON(w, http.StatusOK, govern.CheckModelVersions(usages))
}
