package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fluxspace.ai/hub/archive"
	"fluxspace.ai/hub/govern"
	"fluxspace.ai/hub/session"
)

func governanceTestServer(t *testing.T) (*httptest.Server, *session.Manager) {
	t.Helper()
	mgr := session.NewManagerWithClock(func() int64 { return 1794000000000 }) // 2026-11
	mux := http.NewServeMux()
	srv := &Server{
		Manager:    mgr,
		Archives:   archive.NewStore(),
		Governance: &Governance{Components: govern.NewMemory()},
	}
	RegisterSessions(mux, srv)
	RegisterArchives(mux, srv)
	RegisterGovernance(mux, srv)
	return httptest.NewServer(mux), mgr
}

func TestGovernancePermissionsAndAuditOverHTTP(t *testing.T) {
	srv, _ := governanceTestServer(t)
	defer srv.Close()
	base := srv.URL + "/hub/companions/comp_a"

	if status, body := doJSON(t, http.MethodGet, base+"/permissions", nil); status != http.StatusNotFound {
		t.Fatalf("unconfigured permissions = %d %v", status, body)
	}
	if status, body := doJSON(t, http.MethodPut, base+"/permissions",
		map[string]any{"tier": "root"}); status != http.StatusBadRequest {
		t.Fatalf("unknown tier = %d %v", status, body)
	}
	if status, body := doJSON(t, http.MethodPut, base+"/permissions",
		map[string]any{"tier": govern.TierRestrictedWrite}); status != http.StatusOK {
		t.Fatalf("set permissions = %d %v", status, body)
	}

	// Allowed: restricted write covers a message.
	status, result := doJSON(t, http.MethodPost, base+"/authorize", map[string]any{
		"operation": "send_message", "required_tier": govern.TierRestrictedWrite,
	})
	if status != http.StatusOK || result["allowed"] != true {
		t.Fatalf("authorize write = %d %v", status, result)
	}

	// Denied: high impact is beyond the configured tier, and the refusal is a
	// 403 carrying a stable code.
	status, result = doJSON(t, http.MethodPost, base+"/authorize", map[string]any{
		"operation": "delete_files", "required_tier": govern.TierHighImpact,
	})
	if status != http.StatusForbidden {
		t.Fatalf("authorize high impact = %d %v", status, result)
	}
	if result["code"] != govern.DenyTierTooLow {
		t.Fatalf("denial code = %v", result["code"])
	}

	// Isolation: another companion never inherits comp_a's grant.
	status, other := doJSON(t, http.MethodPost, srv.URL+"/hub/companions/comp_b/authorize", map[string]any{
		"operation": "send_message", "required_tier": govern.TierReadOnly,
	})
	if status != http.StatusForbidden || other["code"] != govern.DenyNoConfig {
		t.Fatalf("comp_b inherited a grant: %d %v", status, other)
	}

	// The audit log records both the allow and the deny.
	status, audit := doJSON(t, http.MethodGet, srv.URL+"/hub/audit?companion_id=comp_a", nil)
	if status != http.StatusOK {
		t.Fatalf("audit list = %d %v", status, audit)
	}
	entries := audit["entries"].([]any)
	outcomes := map[string]bool{}
	for _, raw := range entries {
		e := raw.(map[string]any)
		outcomes[e["outcome"].(string)] = true
	}
	if !outcomes[govern.OutcomeAllowed] || !outcomes[govern.OutcomeDenied] {
		t.Fatalf("audit must record refusals as well as successes: %v", entries)
	}
	if status, verified := doJSON(t, http.MethodGet, srv.URL+"/hub/audit/verify", nil); status != http.StatusOK ||
		verified["verified"] != true {
		t.Fatalf("audit verify = %d %v", status, verified)
	}
}

func TestGovernanceHighImpactNeedsApprovalOverHTTP(t *testing.T) {
	srv, _ := governanceTestServer(t)
	defer srv.Close()
	base := srv.URL + "/hub/companions/comp_a"

	if status, body := doJSON(t, http.MethodPut, base+"/permissions",
		map[string]any{"tier": govern.TierHighImpact}); status != http.StatusOK {
		t.Fatalf("set high-impact tier = %d %v", status, body)
	}
	status, result := doJSON(t, http.MethodPost, base+"/authorize", map[string]any{
		"operation": "delete_files", "required_tier": govern.TierHighImpact,
	})
	if status != http.StatusForbidden || result["code"] != govern.DenyApprovalNeeded {
		t.Fatalf("high impact without approval = %d %v", status, result)
	}

	status, approval := doJSON(t, http.MethodPost, base+"/approvals", map[string]any{
		"operation": "delete_files", "ttl_seconds": 60,
	})
	if status != http.StatusCreated {
		t.Fatalf("approval = %d %v", status, approval)
	}

	status, first := doJSON(t, http.MethodPost, base+"/authorize", map[string]any{
		"operation": "delete_files", "required_tier": govern.TierHighImpact,
	})
	if status != http.StatusOK || first["allowed"] != true {
		t.Fatalf("approved call = %d %v", status, first)
	}
	status, second := doJSON(t, http.MethodPost, base+"/authorize", map[string]any{
		"operation": "delete_files", "required_tier": govern.TierHighImpact,
	})
	if status != http.StatusForbidden || second["code"] != govern.DenyApprovalNeeded {
		t.Fatalf("approval was reusable: %d %v", status, second)
	}
}

func TestGovernanceBudgetDowngradeOverHTTP(t *testing.T) {
	srv, _ := governanceTestServer(t)
	defer srv.Close()
	base := srv.URL + "/hub/companions/comp_a"

	if status, body := doJSON(t, http.MethodPut, base+"/budget",
		map[string]any{"monthly_tokens": 0}); status != http.StatusBadRequest {
		t.Fatalf("zero budget = %d %v", status, body)
	}
	if status, body := doJSON(t, http.MethodPut, base+"/budget",
		map[string]any{"monthly_tokens": 1000}); status != http.StatusOK {
		t.Fatalf("set budget = %d %v", status, body)
	}

	status, decision := doJSON(t, http.MethodPost, base+"/budget/usage", map[string]any{"tokens": 600})
	if status != http.StatusOK || decision["allowed"] != true {
		t.Fatalf("first usage = %d %v", status, decision)
	}

	status, exceeded := doJSON(t, http.MethodPost, base+"/budget/usage", map[string]any{"tokens": 500})
	if status != http.StatusOK {
		t.Fatalf("an over-budget answer is a decision, not a transport error: %d", status)
	}
	if exceeded["allowed"] != false || exceeded["downgrade"] != govern.DowngradeRuleEngine {
		t.Fatalf("over budget = %v", exceeded)
	}
	if !strings.Contains(exceeded["reason"].(string), "rule engine") {
		t.Fatalf("refusal must name the downgrade: %v", exceeded["reason"])
	}

	status, usage := doJSON(t, http.MethodGet, base+"/budget", nil)
	if status != http.StatusOK {
		t.Fatalf("get budget = %d", status)
	}
	if usage["spent_tokens"].(float64) != 600 {
		t.Fatalf("a refused request booked tokens: %v", usage)
	}
	if usage["remaining_tokens"].(float64) != 400 {
		t.Fatalf("remaining = %v", usage["remaining_tokens"])
	}
}

func TestGovernanceModelConsistencyOverHTTP(t *testing.T) {
	srv, mgr := governanceTestServer(t)
	defer srv.Close()
	sessionID := finishedSession(t, mgr, "inv_model_check", 0)

	// No enrichment: the Hub has no model version, so it must say unknown.
	status, created := doJSON(t, http.MethodPost, srv.URL+"/hub/sessions/"+sessionID+"/archive", nil)
	if status != http.StatusCreated {
		t.Fatalf("archive = %d %v", status, created)
	}
	archiveID := created["summary"].(map[string]any)["id"].(string)
	status, check := doJSON(t, http.MethodGet, srv.URL+"/hub/archives/"+archiveID+"/model-consistency", nil)
	if status != http.StatusOK {
		t.Fatalf("model check = %d %v", status, check)
	}
	if check["consistent"] != false || check["code"] != govern.ModelVersionAbsent {
		t.Fatalf("unknown versions must not pass: %v", check)
	}

	// Enriched seats with one shared version: consistent.
	second := finishedSession(t, mgr, "inv_model_check", 0)
	seats := []map[string]any{
		{"seat_id": "seat_0", "persona_id": "lin-shen", "model_version": "claude-sonnet-4-20250514"},
		{"seat_id": "seat_1", "persona_id": "su-wan", "model_version": "claude-sonnet-4-20250514"},
		{"seat_id": "seat_2", "persona_id": "gu-yuan", "model_version": "official-v1"},
		{"seat_id": "seat_3", "persona_id": "shen-mo", "model_version": "official-v1"},
	}
	status, created = doJSON(t, http.MethodPost, srv.URL+"/hub/sessions/"+second+"/archive",
		map[string]any{"seats": seats})
	if status != http.StatusCreated {
		t.Fatalf("enriched archive = %d %v", status, created)
	}
	enrichedID := created["summary"].(map[string]any)["id"].(string)
	status, check = doJSON(t, http.MethodGet, srv.URL+"/hub/archives/"+enrichedID+"/model-consistency", nil)
	if status != http.StatusOK || check["consistent"] != true {
		t.Fatalf("consistent versions = %d %v", status, check)
	}

	// A mixed declaration is reported as mixed, naming both versions.
	third := finishedSession(t, mgr, "inv_model_check", 0)
	seats[1]["model_version"] = "claude-opus-4-20250514"
	status, created = doJSON(t, http.MethodPost, srv.URL+"/hub/sessions/"+third+"/archive",
		map[string]any{"seats": seats, "archive_id": "replay_mixed"})
	if status != http.StatusCreated {
		t.Fatalf("mixed archive = %d %v", status, created)
	}
	status, check = doJSON(t, http.MethodGet, srv.URL+"/hub/archives/replay_mixed/model-consistency", nil)
	if status != http.StatusOK || check["consistent"] != false {
		t.Fatalf("mixed versions = %d %v", status, check)
	}
	if check["code"] != govern.ModelMixedVersions {
		t.Fatalf("mixed code = %v", check["code"])
	}
}

func TestGovernanceUnconfiguredIsExplicit(t *testing.T) {
	mux := http.NewServeMux()
	RegisterGovernance(mux, &Server{Manager: session.NewManager()})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	status, body := doJSON(t, http.MethodGet, srv.URL+"/hub/companions/comp_a/permissions", nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured governance = %d %v", status, body)
	}
	if code := nested(body, "error", "code"); code != "GOVERNANCE_UNCONFIGURED" {
		t.Fatalf("code = %v", code)
	}
}

// governanceServerAt starts a Hub whose governance state is backed by dir, so a
// test can restart it and check what survived.
func governanceServerAt(t *testing.T, dir string) (*httptest.Server, *Governance, func()) {
	t.Helper()
	components, err := govern.Open(dir)
	if err != nil {
		t.Fatalf("open governance state in %s: %v", dir, err)
	}
	if note := components.LoadNote(); note != nil {
		t.Logf("governance load note: %v", note)
	}
	clock := func() int64 { return 1794000000000 } // 2026-11
	mux := http.NewServeMux()
	srv := &Server{
		Manager:    session.NewManagerWithClock(clock),
		Archives:   archive.NewStore(),
		Governance: &Governance{Components: components},
		Clock:      clock,
	}
	RegisterGovernance(mux, srv)
	httpSrv := httptest.NewServer(mux)
	stop := func() {
		httpSrv.Close()
		if err := components.Store.Close(); err != nil {
			t.Logf("close governance store: %v", err)
		}
	}
	t.Cleanup(stop)
	return httpSrv, srv.Governance, stop
}

func TestGovernanceSurvivesHubRestartOverHTTP(t *testing.T) {
	dir := t.TempDir()
	first, _, stopFirst := governanceServerAt(t, dir)
	base := first.URL + "/hub/companions/comp_r"

	if status, body := doJSON(t, http.MethodPut, base+"/permissions",
		map[string]any{"tier": govern.TierRestrictedWrite}); status != http.StatusOK {
		t.Fatalf("set permissions = %d %v", status, body)
	}
	if status, body := doJSON(t, http.MethodPut, base+"/budget",
		map[string]any{"monthly_tokens": 900}); status != http.StatusOK {
		t.Fatalf("set budget = %d %v", status, body)
	}
	if status, decision := doJSON(t, http.MethodPost, base+"/budget/usage",
		map[string]any{"tokens": 150}); status != http.StatusOK || decision["allowed"] != true {
		t.Fatalf("record usage = %d %v", status, decision)
	}
	stopFirst()

	// A fresh Hub process over the same directory: the permission, the spend and
	// the audit chain must all be the ones written before.
	second, _, _ := governanceServerAt(t, dir)
	base = second.URL + "/hub/companions/comp_r"
	status, cfg := doJSON(t, http.MethodGet, base+"/permissions", nil)
	if status != http.StatusOK || cfg["tier"] != govern.TierRestrictedWrite {
		t.Fatalf("permissions after restart = %d %v", status, cfg)
	}

	status, budget := doJSON(t, http.MethodGet, base+"/budget", nil)
	if status != http.StatusOK {
		t.Fatalf("budget after restart = %d %v", status, budget)
	}
	if spent, _ := budget["spent_tokens"].(float64); spent != 150 {
		t.Fatalf("spent after restart = %v, want 150", budget["spent_tokens"])
	}
	if limit, _ := budget["monthly_tokens"].(float64); limit != 900 {
		t.Fatalf("limit after restart = %v, want 900", budget["monthly_tokens"])
	}

	status, audit := doJSON(t, http.MethodGet, second.URL+"/hub/audit", nil)
	if status != http.StatusOK {
		t.Fatalf("audit after restart = %d %v", status, audit)
	}
	if count, _ := audit["count"].(float64); count != 3 {
		t.Fatalf("audit entries after restart = %v, want 3", audit["count"])
	}
	status, verify := doJSON(t, http.MethodGet, second.URL+"/hub/audit/verify", nil)
	if status != http.StatusOK || verify["verified"] != true {
		t.Fatalf("restored chain does not verify: %d %v", status, verify)
	}
}

func TestGovernanceDegradesWhenADecisionCannotBeRecorded(t *testing.T) {
	dir := t.TempDir()
	ts, gov, _ := governanceServerAt(t, dir)
	base := ts.URL + "/hub/companions/comp_s"

	// Break the durable half without stopping the server: the next decision
	// cannot be recorded, so the Hub must refuse to make it.
	if err := gov.Store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	status, body := doJSON(t, http.MethodPut, base+"/permissions",
		map[string]any{"tier": govern.TierReadOnly})
	if status != http.StatusInternalServerError {
		t.Fatalf("recording failure = %d %v, want 500", status, body)
	}
	if code := nested(body, "error", "code"); code != "GOVERNANCE_PERSIST_FAILED" {
		t.Fatalf("code = %v", code)
	}
	if gov.Degraded() == nil {
		t.Fatal("a failed write must degrade governance")
	}

	status, body = doJSON(t, http.MethodGet, base+"/permissions", nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("degraded governance = %d %v, want 503", status, body)
	}
	if code := nested(body, "error", "code"); code != "GOVERNANCE_DEGRADED" {
		t.Fatalf("code = %v", code)
	}
}
