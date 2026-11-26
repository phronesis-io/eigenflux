package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"fluxspace.ai/hub/models"
	"fluxspace.ai/hub/session"
)

func newTestServer(t *testing.T) (*httptest.Server, *session.Manager) {
	t.Helper()
	mgr := session.NewManagerWithClock(func() int64 { return 1700000000000 })
	api := &Server{
		Manager: mgr, SpectatorToken: "spectator-secret",
		PollInterval: 5 * time.Millisecond,
		Clock:        func() int64 { return 1700000000000 },
	}
	mux := http.NewServeMux()
	RegisterHealth(mux, api)
	RegisterSessions(mux, api)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, mgr
}

func call(t *testing.T, method, url string, body any, wantStatus int, out any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s = %d, want %d; body: %s", method, url, resp.StatusCode, wantStatus, payload)
	}
	if out != nil {
		if err := json.Unmarshal(payload, out); err != nil {
			t.Fatalf("decode %s: %v; body: %s", url, err, payload)
		}
	}
}

func invitation(id string) models.Invitation {
	return models.Invitation{
		Kind:             "fluxspace.invitation.v1",
		InvitationID:     id,
		FromAgentID:      "agent_alpha",
		FromCompanionID:  "comp_alpha",
		ToAgentID:        "agent_beta",
		Proposals:        []models.Proposal{{Index: 1, Type: "deduction", Seats: 4, Minutes: 12}},
		SelectedProposal: 1,
		SocialContractDraft: models.SocialContract{
			Sharing: "open", ReplayConsent: "all", Visibility: "private",
		},
		EstimatedMinutes: 12,
	}
}

func claims() []models.SeatClaim {
	return []models.SeatClaim{
		{SeatID: "seat_0", OwnerID: "owner_alpha", AgentID: "agent_alpha", CompanionID: "comp_alpha", DisplayName: "Lin Shen"},
		{SeatID: "seat_1", OwnerID: "owner_beta", AgentID: "agent_beta", CompanionID: "comp_beta", DisplayName: "Su Wan"},
		{SeatID: "seat_2", OwnerID: "owner_official_a", AgentID: "agent_official_a", CompanionID: "comp_official_a", DisplayName: "Official Gu Yuan", Official: true},
		{SeatID: "seat_3", OwnerID: "owner_official_b", AgentID: "agent_official_b", CompanionID: "comp_official_b", DisplayName: "Official Shen Mo", Official: true},
	}
}

func createSession(t *testing.T, srv *httptest.Server, id string) models.SessionView {
	t.Helper()
	req := session.CreateRequest{Invitation: invitation(id), Claims: claims(), Seed: 42}
	var view models.SessionView
	call(t, http.MethodPost, srv.URL+"/hub/sessions", req, http.StatusCreated, &view)
	return view
}

// joinAllHTTP joins every seat and returns the seat stream tokens, because a
// participant stream is no longer readable from the seat id alone (ADR-0009).
func joinAllHTTP(t *testing.T, srv *httptest.Server, id string) map[string]string {
	t.Helper()
	tokens := make(map[string]string, len(claims()))
	for _, c := range claims() {
		var joined struct {
			Session     models.SessionView `json:"session"`
			StreamToken string             `json:"stream_token"`
		}
		call(t, http.MethodPost, srv.URL+"/hub/sessions/"+id+"/join",
			map[string]string{"seat_id": c.SeatID, "owner_id": c.OwnerID}, http.StatusOK, &joined)
		if joined.StreamToken == "" {
			t.Fatalf("join for seat %s returned no stream_token", c.SeatID)
		}
		tokens[c.SeatID] = joined.StreamToken
	}
	return tokens
}

// runHTTPMatch drives the complete S1 loop over HTTP: create, join, then five
// rounds of discuss -> act -> four validated decisions -> automatic resolve.
func runHTTPMatch(t *testing.T, srv *httptest.Server, id string) models.SessionView {
	t.Helper()
	for step := 0; step < 20; step++ {
		var view models.SessionView
		call(t, http.MethodGet, srv.URL+"/hub/sessions/"+id, nil, http.StatusOK, &view)
		if view.Status == models.SessionFinished {
			return view
		}
		call(t, http.MethodPost, srv.URL+"/hub/sessions/"+id+"/advance",
			map[string]string{"phase": "discuss"}, http.StatusOK, &view)
		call(t, http.MethodPost, srv.URL+"/hub/sessions/"+id+"/discussion",
			models.DiscussionMessage{SeatID: "seat_0", Text: "The ledger disagrees with the timetable.", Channel: "public"},
			http.StatusAccepted, nil)
		call(t, http.MethodPost, srv.URL+"/hub/sessions/"+id+"/advance",
			map[string]string{"phase": "act"}, http.StatusOK, &view)
		actionID := view.Skeleton.Rounds[view.Round-1].Actions[0].ID
		for _, c := range claims() {
			call(t, http.MethodPost, srv.URL+"/hub/sessions/"+id+"/actions", map[string]any{
				"seat_id": c.SeatID, "action_id": actionID,
				"reasoning_summary": "rule engine selected a legal action",
				"confidence":        70, "source": "rule_engine",
			}, http.StatusOK, &view)
		}
	}
	t.Fatal("match did not finish over HTTP")
	return models.SessionView{}
}

func TestSessionHTTPEndToEndFiveRounds(t *testing.T) {
	srv, _ := newTestServer(t)
	view := createSession(t, srv, "inv_http_e2e")
	if view.Status != models.SessionLobby || view.Phase != models.PhaseObserve {
		t.Fatalf("initial view = %+v", view)
	}
	if len(view.OfficialSeatIDs) != 2 {
		t.Fatalf("official companions not labelled: %v", view.OfficialSeatIDs)
	}
	joinAllHTTP(t, srv, view.SessionID)

	final := runHTTPMatch(t, srv, view.SessionID)
	if final.Status != models.SessionFinished {
		t.Fatalf("status = %s", final.Status)
	}
	switch final.Outcome {
	case "win", "lose", "partial", "regret":
	default:
		t.Fatalf("outcome %q is neutral", final.Outcome)
	}
	if final.Round != 5 {
		t.Fatalf("final round = %d, want 5", final.Round)
	}
	if final.LastEventID <= 0 {
		t.Fatal("no events recorded")
	}
}

func TestHubHealthStillWorksAlongsideSessions(t *testing.T) {
	srv, _ := newTestServer(t)
	var body map[string]string
	call(t, http.MethodGet, srv.URL+"/health", nil, http.StatusOK, &body)
	if body["status"] != "ok" {
		t.Fatalf("health = %v", body)
	}
}

func TestHTTPErrorShapes(t *testing.T) {
	srv, _ := newTestServer(t)
	var errBody apiError
	call(t, http.MethodGet, srv.URL+"/hub/sessions/missing", nil, http.StatusNotFound, &errBody)
	if errBody.Error.Code != "SESSION_NOT_FOUND" {
		t.Fatalf("error body = %+v", errBody)
	}

	view := createSession(t, srv, "inv_errors")
	joinAllHTTP(t, srv, view.SessionID)
	call(t, http.MethodPost, srv.URL+"/hub/sessions/"+view.SessionID+"/advance",
		map[string]string{"phase": "discuss"}, http.StatusOK, nil)
	call(t, http.MethodPost, srv.URL+"/hub/sessions/"+view.SessionID+"/advance",
		map[string]string{"phase": "act"}, http.StatusOK, nil)
	call(t, http.MethodPost, srv.URL+"/hub/sessions/"+view.SessionID+"/actions", map[string]any{
		"seat_id": "seat_0", "action_id": "not_in_pool", "confidence": 50,
	}, http.StatusBadRequest, &errBody)
	if errBody.Error.Code != "ILLEGAL_ACTION_ID" {
		t.Fatalf("error body = %+v", errBody)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/hub/sessions",
		strings.NewReader(`{"unexpected":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field = %d, want 400", resp.StatusCode)
	}
}

func parseSSE(t *testing.T, body string) []models.Event {
	t.Helper()
	var out []models.Event
	for _, frame := range strings.Split(body, "\n\n") {
		for _, line := range strings.Split(frame, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var ev models.Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				t.Fatalf("decode SSE frame: %v; frame: %s", err, frame)
			}
			out = append(out, ev)
		}
	}
	return out
}

func getSSE(t *testing.T, url string, headers map[string]string, lastEventID string) (int, string) {
	t.Helper()
	if lastEventID != "" {
		separator := "?"
		if strings.Contains(url, "?") {
			separator = "&"
		}
		url += separator + "last_event_id=" + lastEventID
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("SSE request: %v", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("SSE read: %v", err)
	}
	return resp.StatusCode, string(payload)
}

func TestSSEStreamsRespectRolesAndLastEventID(t *testing.T) {
	srv, _ := newTestServer(t)
	view := createSession(t, srv, "inv_sse")
	tokens := joinAllHTTP(t, srv, view.SessionID)
	_ = runHTTPMatch(t, srv, view.SessionID)

	status, body := getSSE(t, srv.URL+"/hub/sessions/"+view.SessionID+"/events?seat_id=seat_1",
		map[string]string{"X-Seat-Token": tokens["seat_1"]}, "")
	if status != http.StatusOK {
		t.Fatalf("participant stream = %d", status)
	}
	participant := parseSSE(t, body)
	if len(participant) == 0 {
		t.Fatal("participant stream returned no events")
	}
	for _, ev := range participant {
		if ev.Visibility == models.VisibilityOmniscient {
			t.Fatalf("omniscient event %d leaked to participant", ev.ID)
		}
	}
	seenEnd := false
	for _, ev := range participant {
		if ev.Type == models.EventMatchEnd {
			seenEnd = true
		}
	}
	if !seenEnd {
		t.Fatal("participant stream missing match_end")
	}

	status, body = getSSE(t, srv.URL+"/hub/sessions/"+view.SessionID+"/spectate",
		map[string]string{"X-Spectator-Token": "spectator-secret"}, "")
	if status != http.StatusOK {
		t.Fatalf("spectator stream = %d", status)
	}
	omniscient := parseSSE(t, body)
	if len(omniscient) <= len(participant) {
		t.Fatalf("omniscient (%d) must be a superset of participant (%d)", len(omniscient), len(participant))
	}
	hasSecret := false
	for _, ev := range omniscient {
		if ev.Visibility == models.VisibilityOmniscient {
			hasSecret = true
		}
	}
	if !hasSecret {
		t.Fatal("spectator stream did not expose omniscient-only reveals")
	}

	cutoff := omniscient[len(omniscient)/2].ID
	_, recovered := getSSE(t, srv.URL+"/hub/sessions/"+view.SessionID+"/spectate",
		map[string]string{"X-Spectator-Token": "spectator-secret"},
		strconv.FormatInt(cutoff, 10))
	resumed := parseSSE(t, recovered)
	if len(resumed) == 0 {
		t.Fatal("Last-Event-ID recovery returned nothing")
	}
	if resumed[0].ID <= cutoff {
		t.Fatalf("recovery started at %d, expected > %d", resumed[0].ID, cutoff)
	}
}

func TestSpectatorStreamRequiresAuth(t *testing.T) {
	mgr := session.NewManager()
	mux := http.NewServeMux()
	RegisterSessions(mux, &Server{Manager: mgr})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/hub/sessions/whatever/spectate")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured spectator auth = %d, want 503", resp.StatusCode)
	}

	configuredMux := http.NewServeMux()
	RegisterSessions(configuredMux, &Server{Manager: mgr, SpectatorToken: "secret"})
	configured := httptest.NewServer(configuredMux)
	defer configured.Close()
	req, _ := http.NewRequest(http.MethodGet, configured.URL+"/hub/sessions/whatever/spectate", nil)
	req.Header.Set("X-Spectator-Token", "wrong")
	bad, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong token = %d, want 403", bad.StatusCode)
	}
}

func TestSnapshotEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)
	view := createSession(t, srv, "inv_snapshot")
	joinAllHTTP(t, srv, view.SessionID)
	var payload struct {
		Session     models.SessionView `json:"session"`
		LastEventID int64              `json:"last_event_id"`
		TS          int64              `json:"ts"`
	}
	call(t, http.MethodGet, srv.URL+"/hub/sessions/"+view.SessionID+"/snapshot", nil, http.StatusOK, &payload)
	if payload.Session.SessionID != view.SessionID || payload.LastEventID == 0 {
		t.Fatalf("snapshot = %+v", payload)
	}
	// The timestamp must come from the server clock, not the wall clock: a
	// response that mixes the two breaks any assertion built on a fixed clock.
	if payload.TS != 1700000000000 {
		t.Fatalf("snapshot ts = %d, want the injected clock's 1700000000000", payload.TS)
	}
}

func TestOversizedBodyIsRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	huge := strings.Repeat("a", maxBodyBytes+1024)
	resp, err := http.Post(srv.URL+"/hub/sessions", "application/json",
		strings.NewReader(`{"invitation_id":"`+huge+`"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized body = %d, want 400", resp.StatusCode)
	}
	var body apiError
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error.Code != "INVALID_BODY" {
		t.Fatalf("code = %q, want INVALID_BODY", body.Error.Code)
	}
}

func TestParticipantStreamNeedsSeatID(t *testing.T) {
	srv, _ := newTestServer(t)
	view := createSession(t, srv, "inv_seat_required")
	resp, err := http.Get(srv.URL + "/hub/sessions/" + view.SessionID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing seat_id = %d, want 400", resp.StatusCode)
	}
}
