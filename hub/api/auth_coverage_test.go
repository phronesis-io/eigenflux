package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"fluxspace.ai/hub/archive"
	"fluxspace.ai/hub/govern"
	invitationpkg "fluxspace.ai/hub/invitation"
	"fluxspace.ai/hub/session"
)

// auditedRoutes is the complete inventory of every method+path registered by
// the seven Register* functions in main.go. It is the single source of truth for
// the auth-coverage tests below: adding a route to a Register* function without
// adding it here is the failure this file is meant to expose.
//
// public marks the closed set of documented exemptions from the owner bearer
// gate (fluxspace-contracts.md §3, ADR-0009 §2.1 addendum):
//
//	GET /health                       open liveness check
//	GET /hub/sessions/{id}/events     per-seat X-Seat-Token
//	GET /hub/sessions/{id}/spectate   X-Spectator-Token
//
// Everything else is default-denied and must return 401 AUTH_REQUIRED when no
// credential is presented. Note that GET /hub/sessions/{id} and /snapshot are
// spectator-readable, but only with a valid X-Spectator-Token; with no
// credential at all they are still default-denied, so public is false for them.
type auditedRoute struct {
	method string
	path   string
	public bool
}

var auditedRoutes = []auditedRoute{
	// health
	{http.MethodGet, "/health", true},
	// sessions
	{http.MethodPost, "/hub/sessions", false},
	{http.MethodPost, "/hub/sessions/s1/join", false},
	{http.MethodPost, "/hub/sessions/s1/actions", false},
	{http.MethodPost, "/hub/sessions/s1/advance", false},
	{http.MethodPost, "/hub/sessions/s1/discussion", false},
	{http.MethodGet, "/hub/sessions/s1", false},
	{http.MethodGet, "/hub/sessions/s1/snapshot", false},
	{http.MethodGet, "/hub/sessions/s1/events", true},
	{http.MethodGet, "/hub/sessions/s1/spectate", true},
	// invitations
	{http.MethodPost, "/hub/invitations", false},
	{http.MethodGet, "/hub/invitations/inv1", false},
	{http.MethodPost, "/hub/invitations/inv1/send", false},
	{http.MethodPost, "/hub/invitations/inv1/reply", false},
	{http.MethodPost, "/hub/invitations/inv1/poll", false},
	{http.MethodPost, "/hub/invitations/inv1/session", false},
	// follow-ups
	{http.MethodGet, "/hub/sessions/s1/followup", false},
	{http.MethodPost, "/hub/sessions/s1/followup", false},
	// archives
	{http.MethodPost, "/hub/sessions/s1/archive", false},
	{http.MethodGet, "/hub/archives", false},
	{http.MethodGet, "/hub/archives/a1", false},
	{http.MethodGet, "/hub/archives/a1/verify", false},
	{http.MethodGet, "/hub/archives/a1/views/full", false},
	{http.MethodPost, "/hub/archives/a1/consent", false},
	{http.MethodPost, "/hub/archives/a1/publish", false},
	{http.MethodPost, "/hub/archives/a1/reprise", false},
	{http.MethodGet, "/hub/archives/a1/reprises", false},
	{http.MethodGet, "/hub/reprises/r1", false},
	{http.MethodPost, "/hub/reprises/r1/complete", false},
	// governance
	{http.MethodGet, "/hub/companions/comp1/permissions", false},
	{http.MethodPut, "/hub/companions/comp1/permissions", false},
	{http.MethodPost, "/hub/companions/comp1/permissions/revoke", false},
	{http.MethodPost, "/hub/companions/comp1/approvals", false},
	{http.MethodPost, "/hub/companions/comp1/authorize", false},
	{http.MethodGet, "/hub/companions/comp1/budget", false},
	{http.MethodPut, "/hub/companions/comp1/budget", false},
	{http.MethodPost, "/hub/companions/comp1/budget/usage", false},
	{http.MethodGet, "/hub/audit", false},
	{http.MethodGet, "/hub/audit/verify", false},
	{http.MethodGet, "/hub/archives/a1/model-consistency", false},
	// companions (ownership registry)
	{http.MethodPost, "/hub/companions", false},
	{http.MethodGet, "/hub/companions/comp1", false},
}

// newAuditedServer registers every route on one mux and wraps it in WithAuth,
// exactly as main.go does, so the audit exercises the real gated surface.
func newAuditedServer(t *testing.T) authedServer {
	t.Helper()
	clock := func() int64 { return 1794000000000 }
	srv := &Server{
		Manager:        session.NewManagerWithClock(clock),
		SpectatorToken: "spectator-secret",
		Invitations:    invitationpkg.NewManager(),
		Archives:       archive.NewStore(),
		Governance:     &Governance{Components: govern.NewMemory()},
		OwnerTokens: map[string]string{
			"tok-alpha": "owner_alpha",
			"tok-beta":  "owner_beta",
		},
		Clock: clock,
	}
	mux := http.NewServeMux()
	RegisterHealth(mux, srv)
	RegisterSessions(mux, srv)
	RegisterInvitations(mux, srv)
	RegisterFollowUps(mux, srv)
	RegisterArchives(mux, srv)
	RegisterGovernance(mux, srv)
	RegisterCompanions(mux, srv)
	httpSrv := httptest.NewServer(WithAuth(mux, srv))
	t.Cleanup(httpSrv.Close)
	return authedServer{url: httpSrv.URL, governor: srv.Governance, manager: srv.Manager}
}

// codeOf extracts an error.code from a JSON body, or "" when there is none.
func codeOf(body map[string]any) string {
	if code, ok := nested(body, "error", "code").(string); ok {
		return code
	}
	return ""
}

// TestRouteAuditDefaultDeny walks the full inventory. Every non-exempt route
// must be default-denied (401 AUTH_REQUIRED with no credential), and the three
// documented public exemptions must NOT be rejected by the owner-token gate.
// This proves that no mutating or privileged endpoint slips past WithAuth.
func TestRouteAuditDefaultDeny(t *testing.T) {
	srv := newAuditedServer(t)
	for _, rt := range auditedRoutes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			status, body := doAuthed(t, rt.method, srv.url+rt.path, "", nil)
			code := codeOf(body)
			if rt.public {
				if code == "AUTH_REQUIRED" || code == "AUTH_INVALID" {
					t.Fatalf("documented public route was owner-gated: %d %v", status, body)
				}
				return
			}
			if status != http.StatusUnauthorized {
				t.Fatalf("no credential = %d %v, want 401 (default-deny)", status, body)
			}
			if code != "AUTH_REQUIRED" {
				t.Fatalf("code = %q, want AUTH_REQUIRED", code)
			}
		})
	}
}

// TestAuthGateIsMethodQualified closes the concrete gap the audit found. The
// three exemptions used to be matched by path with no method restriction, so a
// non-GET request to /health, .../events or .../spectate skipped the token gate
// entirely (it reached the mux as an unauthenticated 405). Exemptions are now
// GET-only: any other method on those exact paths must fall through to
// default-deny and return 401 without a credential.
func TestAuthGateIsMethodQualified(t *testing.T) {
	srv := newAuditedServer(t)
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/health"},
		{http.MethodPut, "/health"},
		{http.MethodDelete, "/health"},
		{http.MethodPost, "/hub/sessions/s1/events"},
		{http.MethodPut, "/hub/sessions/s1/events"},
		{http.MethodPost, "/hub/sessions/s1/spectate"},
		{http.MethodDelete, "/hub/sessions/s1/spectate"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			status, body := doAuthed(t, tc.method, srv.url+tc.path, "", nil)
			if status != http.StatusUnauthorized || codeOf(body) != "AUTH_REQUIRED" {
				t.Fatalf("non-GET request bypassed default-deny: %d %v", status, body)
			}
		})
	}
}

// TestSpectatorCannotReachMutatingRoutes presents a valid spectator token to
// every mutating route in the inventory. The spectator token is read-only: it
// must never authenticate a write, so each of these is 401. (The pre-existing
// auth_test.go covered only four session writes; this covers the whole surface.)
func TestSpectatorCannotReachMutatingRoutes(t *testing.T) {
	srv := newAuditedServer(t)
	for _, rt := range auditedRoutes {
		if rt.public || rt.method == http.MethodGet {
			continue
		}
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			status, body := doSpectator(t, rt.method, srv.url+rt.path, "spectator-secret")
			if status != http.StatusUnauthorized {
				t.Fatalf("spectator reached a mutating route: %d %v", status, body)
			}
		})
	}
}

// TestAuthScenarios pins the four canonical auth outcomes on real resources:
// 401 with no credential, 403 for a wrong owner, 404 for an unknown resource,
// and 200 for an authorised owner. A companion is bound through the public API
// first so the ownership checks have a real subject.
func TestAuthScenarios(t *testing.T) {
	srv := newAuditedServer(t)
	if status, body := doAuthed(t, http.MethodPost, srv.url+"/hub/companions", "tok-alpha",
		map[string]any{"companion_id": "comp_alpha"}); status != http.StatusCreated {
		t.Fatalf("bind companion to owner_alpha = %d %v", status, body)
	}

	for _, tc := range []struct {
		name       string
		method     string
		path       string
		token      string
		wantStatus int
		wantCode   string
	}{
		{"401 no credential", http.MethodGet, "/hub/audit", "", http.StatusUnauthorized, "AUTH_REQUIRED"},
		{"403 wrong owner", http.MethodGet, "/hub/companions/comp_alpha/permissions", "tok-beta", http.StatusForbidden, "COMPANION_NOT_YOURS"},
		{"404 unknown resource", http.MethodGet, "/hub/companions/never_bound/permissions", "tok-alpha", http.StatusNotFound, "COMPANION_NOT_REGISTERED"},
		{"200 authorized", http.MethodGet, "/hub/audit", "tok-alpha", http.StatusOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := doAuthed(t, tc.method, srv.url+tc.path, tc.token, nil)
			if status != tc.wantStatus {
				t.Fatalf("status = %d %v, want %d", status, body, tc.wantStatus)
			}
			if code := codeOf(body); code != tc.wantCode {
				t.Fatalf("error code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}
