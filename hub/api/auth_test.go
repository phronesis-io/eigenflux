package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"fluxspace.ai/hub/archive"
	"fluxspace.ai/hub/govern"
	"fluxspace.ai/hub/session"
)

// authedServer is a Hub with owner authentication switched on. The unauthenticated
// mode is covered by every other api test, which is exactly the problem this file
// exists to solve: the gated path had no tests at all.
type authedServer struct {
	url      string
	governor *Governance
}

func newAuthedServer(t *testing.T) authedServer {
	t.Helper()
	clock := func() int64 { return 1794000000000 }
	components := govern.NewMemory()
	tokens := map[string]string{
		"tok-alpha": "owner_alpha",
		"tok-beta":  "owner_beta",
	}
	api := &Server{
		Manager:        session.NewManagerWithClock(clock),
		SpectatorToken: "spectator-secret",
		Archives:       archive.NewStore(),
		Governance:     &Governance{Components: components},
		OwnerTokens:    tokens,
		Clock:          clock,
	}
	mux := http.NewServeMux()
	RegisterHealth(mux, api)
	RegisterSessions(mux, api)
	RegisterArchives(mux, api)
	RegisterGovernance(mux, api)
	RegisterCompanions(mux, api)
	srv := httptest.NewServer(WithAuth(mux, api))
	t.Cleanup(srv.Close)
	return authedServer{url: srv.URL, governor: api.Governance}
}

func doAuthed(t *testing.T, method, url, token string, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAuthGateRejectsMissingAndWrongTokens(t *testing.T) {
	srv := newAuthedServer(t)
	url := srv.url + "/hub/audit"

	if status, body := doAuthed(t, http.MethodGet, url, "", nil); status != http.StatusUnauthorized {
		t.Fatalf("no token = %d %v", status, body)
	} else if code := nested(body, "error", "code"); code != "AUTH_REQUIRED" {
		t.Fatalf("code = %v", code)
	}

	if status, body := doAuthed(t, http.MethodGet, url, "tok-wrong", nil); status != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d %v", status, body)
	} else if code := nested(body, "error", "code"); code != "AUTH_INVALID" {
		t.Fatalf("code = %v", code)
	}

	if status, body := doAuthed(t, http.MethodGet, url, "tok-alpha", nil); status != http.StatusOK {
		t.Fatalf("valid token = %d %v", status, body)
	}
}

// /health, /spectate and /events keep their own mechanisms; gating them behind an
// owner token would have broken the deployment check and the spectator role.
func TestAuthGateLeavesHealthAndStreamRoutesAlone(t *testing.T) {
	srv := newAuthedServer(t)
	if status, body := doAuthed(t, http.MethodGet, srv.url+"/health", "", nil); status != http.StatusOK {
		t.Fatalf("health without a token = %d %v", status, body)
	} else if body["auth"] != "enabled" {
		t.Fatalf("health auth = %v, want enabled", body["auth"])
	}

	// /events is exempt from owner auth but enforces the seat token, so an
	// unauthenticated request reaches the seat check rather than the token gate.
	if status, body := doAuthed(t, http.MethodGet, srv.url+"/hub/sessions/nope/events?seat_id=seat_0", "", nil); status != http.StatusNotFound {
		t.Fatalf("events without owner token = %d %v, want the seat-level 404", status, body)
	}
}

func TestCompanionBindingAndOwnershipGate(t *testing.T) {
	srv := newAuthedServer(t)
	base := srv.url + "/hub/companions"

	// Unbound: governance refuses before it considers a tier.
	if status, body := doAuthed(t, http.MethodPut, base+"/comp_x/permissions", "tok-alpha",
		map[string]any{"tier": govern.TierReadOnly}); status != http.StatusNotFound {
		t.Fatalf("unbound companion = %d %v", status, body)
	} else if code := nested(body, "error", "code"); code != "COMPANION_NOT_REGISTERED" {
		t.Fatalf("code = %v", code)
	}

	if status, body := doAuthed(t, http.MethodPost, base, "tok-alpha",
		map[string]any{"companion_id": "comp_x"}); status != http.StatusCreated {
		t.Fatalf("bind = %d %v", status, body)
	} else if body["owner_id"] != "owner_alpha" {
		t.Fatalf("binding owner = %v", body["owner_id"])
	}

	// The owner can configure it.
	if status, body := doAuthed(t, http.MethodPut, base+"/comp_x/permissions", "tok-alpha",
		map[string]any{"tier": govern.TierRestrictedWrite}); status != http.StatusOK {
		t.Fatalf("own companion = %d %v", status, body)
	}

	// Another owner cannot read it, configure it, or spend its budget.
	if status, body := doAuthed(t, http.MethodGet, base+"/comp_x/permissions", "tok-beta", nil); status != http.StatusForbidden {
		t.Fatalf("other owner read = %d %v", status, body)
	} else if code := nested(body, "error", "code"); code != "COMPANION_NOT_YOURS" {
		t.Fatalf("code = %v", code)
	}
	if status, _ := doAuthed(t, http.MethodPut, base+"/comp_x/budget", "tok-beta",
		map[string]any{"monthly_tokens": 10}); status != http.StatusForbidden {
		t.Fatalf("other owner budget = %d", status)
	}

	// Rebinding to a different owner is refused rather than transferred.
	if status, body := doAuthed(t, http.MethodPost, base, "tok-beta",
		map[string]any{"companion_id": "comp_x"}); status != http.StatusConflict {
		t.Fatalf("rebind = %d %v", status, body)
	}

	// Rebinding to the same owner is idempotent.
	if status, _ := doAuthed(t, http.MethodPost, base, "tok-alpha",
		map[string]any{"companion_id": "comp_x"}); status != http.StatusCreated {
		t.Fatalf("idempotent rebind = %d", status)
	}
}

func TestAuditIsAttributedToTheOwnerAndScopedToIt(t *testing.T) {
	srv := newAuthedServer(t)
	base := srv.url + "/hub/companions"
	if status, body := doAuthed(t, http.MethodPost, base, "tok-alpha",
		map[string]any{"companion_id": "comp_a1"}); status != http.StatusCreated {
		t.Fatalf("bind = %d %v", status, body)
	}
	if status, body := doAuthed(t, http.MethodPost, base, "tok-beta",
		map[string]any{"companion_id": "comp_b1"}); status != http.StatusCreated {
		t.Fatalf("bind = %d %v", status, body)
	}

	status, body := doAuthed(t, http.MethodGet, srv.url+"/hub/audit", "tok-alpha", nil)
	if status != http.StatusOK {
		t.Fatalf("audit = %d %v", status, body)
	}
	entries, _ := body["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("owner sees %d entries, want only its own 1", len(entries))
	}
	first, _ := entries[0].(map[string]any)
	if first["owner_id"] != "owner_alpha" || first["companion_id"] != "comp_a1" {
		t.Fatalf("entry = %v", first)
	}

	// The chain still verifies with owner ids in the preimage.
	if status, body := doAuthed(t, http.MethodGet, srv.url+"/hub/audit/verify", "tok-alpha", nil); status != http.StatusOK {
		t.Fatalf("verify = %d %v", status, body)
	} else if body["verified"] != true {
		t.Fatalf("verified = %v", body["verified"])
	}
}

func TestJoinRequiresTheOwnersTokenAndIssuesASeatToken(t *testing.T) {
	srv := newAuthedServer(t)
	create := session.CreateRequest{Invitation: invitation("inv_auth_join"), Claims: claims(), Seed: 42}
	status, body := doAuthed(t, http.MethodPost, srv.url+"/hub/sessions", "tok-alpha", create)
	if status != http.StatusCreated {
		t.Fatalf("create = %d %v", status, body)
	}
	sessionView, _ := body["session_id"].(string)
	if sessionView == "" {
		t.Fatalf("create returned no session_id: %v", body)
	}

	// seat_1 belongs to owner_beta; alpha's token must not join it.
	if status, body := doAuthed(t, http.MethodPost, srv.url+"/hub/sessions/"+sessionView+"/join",
		"tok-alpha", map[string]string{"seat_id": "seat_1", "owner_id": "owner_beta"}); status != http.StatusForbidden {
		t.Fatalf("cross-owner join = %d %v", status, body)
	}

	// The owner joins its own seat and receives a stream token, which is the only
	// way to read that seat's private events afterwards.
	status, joined := doAuthed(t, http.MethodPost, srv.url+"/hub/sessions/"+sessionView+"/join",
		"tok-beta", map[string]string{"seat_id": "seat_1", "owner_id": "owner_beta"})
	if status != http.StatusOK {
		t.Fatalf("own join = %d %v", status, joined)
	}
	token, _ := joined["stream_token"].(string)
	if token == "" {
		t.Fatalf("join returned no stream_token: %v", joined)
	}

	events := srv.url + "/hub/sessions/" + sessionView + "/events?seat_id=seat_1"
	if status, body := doAuthed(t, http.MethodGet, events, "", nil); status != http.StatusUnauthorized {
		t.Fatalf("stream without seat token = %d %v", status, body)
	}
	req, err := http.NewRequest(http.MethodGet, events, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("X-Seat-Token", token)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream with seat token = %d", resp.StatusCode)
	}
}
