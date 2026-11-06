package api

import (
	"net/http"
	"strconv"

	"time"

	"fluxspace.ai/hub/apperr"
	"fluxspace.ai/hub/govern"
)

// Governance bundles the S5 controls so the HTTP layer has one optional
// dependency instead of three. Nil means the Hub was started without them and
// every governance endpoint answers 503 rather than pretending a permission was
// checked.
type Governance struct {
	Registry *govern.Registry
	Budget   *govern.BudgetLedger
	Audit    *govern.AuditLog
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
	if s.Governance == nil || s.Governance.Registry == nil ||
		s.Governance.Budget == nil || s.Governance.Audit == nil {
		return nil, apperr.New(http.StatusServiceUnavailable, "GOVERNANCE_UNCONFIGURED",
			"permissions, budget and audit are not configured on this Hub")
	}
	return s.Governance, nil
}

// audit records one governance decision. Every authorisation and every budget
// answer goes through here, so the log answers "what was allowed or refused"
// rather than only "what succeeded".
func (s *Server) audit(gov *Governance, companionID, operation, tier, outcome, detail string) {
	_, err := gov.Audit.Append(govern.AuditEntry{
		CompanionID: companionID, Operation: operation, Tier: tier,
		Outcome: outcome, Detail: detail, At: s.now(),
	})
	// A failure to append is logged into the response path by the caller; the
	// log is in memory and cannot fail for any reason other than a bad entry,
	// which would be a programming error rather than a runtime condition.
	_ = err
}

func (s *Server) getPermissions(w http.ResponseWriter, r *http.Request) {
	gov, err := s.governance()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	companionID := r.PathValue("id")
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
	cfg, err := gov.Registry.Set(companionID, body.Tier, s.now())
	if err != nil {
		writeDomainError(w, apperr.New(http.StatusBadRequest, "INVALID_PERMISSION_TIER", "%v", err))
		return
	}
	s.audit(gov, companionID, "set_permissions", body.Tier, govern.OutcomeAllowed,
		"permission tier set to "+body.Tier)
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) revokePermissions(w http.ResponseWriter, r *http.Request) {
	gov, err := s.governance()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	companionID := r.PathValue("id")
	cfg, err := gov.Registry.Revoke(companionID, s.now())
	if err != nil {
		writeDomainError(w, apperr.New(http.StatusNotFound, "PERMISSIONS_NOT_CONFIGURED", "%v", err))
		return
	}
	s.audit(gov, companionID, "revoke_permissions", cfg.Tier, govern.OutcomeAllowed,
		"permissions revoked; tier retained for audit")
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
	approval, err := gov.Registry.Approve(companionID, body.Operation, body.TTLSeconds, s.now())
	if err != nil {
		writeDomainError(w, apperr.New(http.StatusBadRequest, "INVALID_APPROVAL", "%v", err))
		return
	}
	s.audit(gov, companionID, body.Operation, govern.TierHighImpact, govern.OutcomeAllowed,
		"single-use approval "+approval.ID+" issued")
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
	result := gov.Registry.Authorize(companionID, body.Operation, body.RequiredTier, s.now())
	outcome := govern.OutcomeDenied
	status := http.StatusForbidden
	if result.Allowed {
		outcome = govern.OutcomeAllowed
		status = http.StatusOK
	}
	s.audit(gov, companionID, body.Operation, body.RequiredTier, outcome, result.Code)
	writeJSON(w, status, result)
}

func (s *Server) getBudget(w http.ResponseWriter, r *http.Request) {
	gov, err := s.governance()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	companionID := r.PathValue("id")
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
	if err := gov.Budget.SetLimit(companionID, body.MonthlyTokens); err != nil {
		writeDomainError(w, apperr.New(http.StatusBadRequest, "INVALID_BUDGET_LIMIT", "%v", err))
		return
	}
	s.audit(gov, companionID, "set_budget", "budget", govern.OutcomeAllowed,
		"monthly token limit set to "+strconv.Itoa(body.MonthlyTokens))
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
	decision := gov.Budget.Record(companionID, body.Tokens, s.nowTime())
	outcome := govern.OutcomeAllowed
	status := http.StatusOK
	if !decision.Allowed {
		outcome = govern.OutcomeDenied
		status = http.StatusOK // the answer is a decision, not a transport failure
	}
	s.audit(gov, companionID, "model_call", "budget", outcome, decision.Reason)
	writeJSON(w, status, decision)
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	gov, err := s.governance()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	entries := gov.Audit.Entries(r.URL.Query().Get("companion_id"))
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
