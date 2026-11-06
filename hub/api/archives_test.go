package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fluxspace.ai/hub/archive"
	"fluxspace.ai/hub/models"
	"fluxspace.ai/hub/session"
)

// finishedSession drives a real match through the Manager and returns its id.
// The HTTP archive endpoints then operate on genuine events, not fixtures.
func finishedSession(t *testing.T, mgr *session.Manager, invitationID string, actionOffset int) string {
	t.Helper()
	invitation := models.Invitation{
		Kind:             "fluxspace.invitation.v1",
		InvitationID:     invitationID,
		FromAgentID:      "agent_alpha",
		FromCompanionID:  "comp_alpha",
		ToAgentID:        "agent_beta",
		Proposals:        []models.Proposal{{Index: 1, Type: "deduction", Seats: 4, Minutes: 12}},
		SelectedProposal: 1,
		SocialContractDraft: models.SocialContract{
			Sharing: "open", PrivateChannels: "allowed",
			ReplayConsent: "all", Visibility: "private",
		},
		EstimatedMinutes: 12,
		ExpiresAt:        1730000300000,
	}
	claims := []models.SeatClaim{
		{SeatID: "seat_0", OwnerID: "owner_alpha", AgentID: "agent_alpha", CompanionID: "comp_alpha", DisplayName: "Lin Shen"},
		{SeatID: "seat_1", OwnerID: "owner_beta", AgentID: "agent_beta", CompanionID: "comp_beta", DisplayName: "Su Wan"},
		{SeatID: "seat_2", OwnerID: "owner_official_a", AgentID: "agent_official_a", CompanionID: "comp_official_a", DisplayName: "Official Gu Yuan", Official: true},
		{SeatID: "seat_3", OwnerID: "owner_official_b", AgentID: "agent_official_b", CompanionID: "comp_official_b", DisplayName: "Official Shen Mo", Official: true},
	}
	view, err := mgr.Create(session.CreateRequest{Invitation: invitation, Claims: claims, Seed: 42})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, claim := range claims {
		if _, _, err := mgr.Join(view.SessionID, claim.SeatID, claim.OwnerID); err != nil {
			t.Fatalf("Join(%s): %v", claim.SeatID, err)
		}
	}
	for step := 0; step < 20; step++ {
		current, err := mgr.State(view.SessionID)
		if err != nil {
			t.Fatalf("State: %v", err)
		}
		if current.Status == models.SessionFinished {
			return view.SessionID
		}
		if _, err := mgr.Advance(view.SessionID, models.PhaseDiscuss); err != nil {
			t.Fatalf("discuss: %v", err)
		}
		if err := mgr.PostDiscussion(view.SessionID, models.DiscussionMessage{
			SeatID: "seat_0", Text: "The ledger disagrees with the timetable.", Channel: "public",
		}); err != nil {
			t.Fatalf("discussion: %v", err)
		}
		if _, err := mgr.Advance(view.SessionID, models.PhaseAct); err != nil {
			t.Fatalf("act: %v", err)
		}
		actions := current.Skeleton.Rounds[current.Round-1].Actions
		actionID := actions[actionOffset%len(actions)].ID
		for _, claim := range claims {
			if _, err := mgr.SubmitAction(view.SessionID, claim.SeatID, models.DecisionSubmission{
				ActionID: actionID, ReasoningSummary: "rule engine picked the top legal action",
				Confidence: 70, Source: "rule_engine",
			}); err != nil {
				t.Fatalf("SubmitAction(%s): %v", claim.SeatID, err)
			}
		}
	}
	t.Fatal("match did not finish")
	return ""
}

func archiveTestServer(t *testing.T) (*httptest.Server, *session.Manager) {
	t.Helper()
	mgr := session.NewManagerWithClock(func() int64 { return 1700000000000 })
	mux := http.NewServeMux()
	srv := &Server{Manager: mgr, Archives: archive.NewStore()}
	RegisterSessions(mux, srv)
	RegisterArchives(mux, srv)
	return httptest.NewServer(mux), mgr
}

func doJSON(t *testing.T, method, url string, body any) (int, map[string]any) {
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s %s returned non-JSON %q: %v", method, url, raw, err)
		}
	}
	return resp.StatusCode, out
}

func TestArchiveHTTPFullS4Flow(t *testing.T) {
	srv, mgr := archiveTestServer(t)
	defer srv.Close()
	sessionID := finishedSession(t, mgr, "inv_http_archive", 0)

	status, created := doJSON(t, http.MethodPost, srv.URL+"/hub/sessions/"+sessionID+"/archive", nil)
	if status != http.StatusCreated {
		t.Fatalf("archive create = %d, body %v", status, created)
	}
	summary := created["summary"].(map[string]any)
	archiveID := summary["id"].(string)
	contentHash := summary["content_hash"].(string)
	if !strings.HasPrefix(contentHash, "sha256:") {
		t.Fatalf("content hash = %q", contentHash)
	}
	if summary["verified"] != true {
		t.Fatalf("summary = %v", summary)
	}

	// The archive must admit what it could not capture.
	integrity := created["integrity"].(map[string]any)
	if integrity["complete"] != false {
		t.Fatalf("archive claims completeness without persona enrichment: %v", integrity)
	}
	missing := integrity["missing_fields"].([]any)
	if len(missing) == 0 {
		t.Fatalf("missing_fields is empty")
	}

	if status, list := doJSON(t, http.MethodGet, srv.URL+"/hub/archives", nil); status != http.StatusOK ||
		len(list["archives"].([]any)) != 1 {
		t.Fatalf("list = %d %v", status, list)
	}

	status, verified := doJSON(t, http.MethodGet, srv.URL+"/hub/archives/"+archiveID+"/verify", nil)
	if status != http.StatusOK || verified["verified"] != true {
		t.Fatalf("verify = %d %v", status, verified)
	}

	// All six modes over HTTP; same_spec needs a partner archive.
	for _, mode := range archive.Modes() {
		url := srv.URL + "/hub/archives/" + archiveID + "/views/" + mode
		switch mode {
		case archive.ModeAccelerated:
			url += "?speed=4"
		case archive.ModeSingleCharacter:
			url += "?seat_id=seat_0"
		case archive.ModeKeyMoments:
			url += "?moment_limit=3"
		case archive.ModeSameSpecComparison:
			if status, _ := doJSON(t, http.MethodGet, url, nil); status != http.StatusUnprocessableEntity {
				t.Fatalf("same_spec without compare_with = %d, want 422", status)
			}
			continue
		}
		status, view := doJSON(t, http.MethodGet, url, nil)
		if status != http.StatusOK {
			t.Fatalf("mode %s = %d %v", mode, status, view)
		}
		if view["mode"] != mode {
			t.Fatalf("mode %s returned %v", mode, view["mode"])
		}
	}

	// Publication is refused until every owner has consented.
	if status, body := doJSON(t, http.MethodPost, srv.URL+"/hub/archives/"+archiveID+"/publish", nil); status != http.StatusForbidden {
		t.Fatalf("publish without consent = %d %v", status, body)
	}
	// A companion that is not a participant cannot consent.
	status, body := doJSON(t, http.MethodPost, srv.URL+"/hub/archives/"+archiveID+"/consent", map[string]any{
		"companion_id": "comp_stranger", "owner_id": "owner_x", "accepted": true,
	})
	if status != http.StatusForbidden {
		t.Fatalf("stranger consent = %d %v", status, body)
	}
	if code := nested(body, "error", "code"); code != "NOT_A_SIGNING_PARTICIPANT" {
		t.Fatalf("code = %v", code)
	}
	// An official backfill has no owner and is not a signer.
	status, _ = doJSON(t, http.MethodPost, srv.URL+"/hub/archives/"+archiveID+"/consent", map[string]any{
		"companion_id": "comp_official_a", "owner_id": "owner_official_a", "accepted": true,
	})
	if status != http.StatusForbidden {
		t.Fatalf("official consent = %d, want 403", status)
	}

	for _, companion := range []string{"comp_alpha", "comp_beta"} {
		status, body := doJSON(t, http.MethodPost, srv.URL+"/hub/archives/"+archiveID+"/consent", map[string]any{
			"companion_id": companion, "owner_id": "owner_" + companion, "accepted": true,
		})
		if status != http.StatusOK {
			t.Fatalf("consent %s = %d %v", companion, status, body)
		}
	}
	status, decision := doJSON(t, http.MethodPost, srv.URL+"/hub/archives/"+archiveID+"/publish", nil)
	if status != http.StatusOK || decision["public"] != true {
		t.Fatalf("publish = %d %v", status, decision)
	}

	// A reprise is authorised by the same unanimous consent.
	status, reprise := doJSON(t, http.MethodPost, srv.URL+"/hub/archives/"+archiveID+"/reprise", nil)
	if status != http.StatusCreated {
		t.Fatalf("reprise = %d %v", status, reprise)
	}
	repriseID := reprise["id"].(string)
	if status, got := doJSON(t, http.MethodGet, srv.URL+"/hub/reprises/"+repriseID, nil); status != http.StatusOK ||
		got["status"] != "authorized" {
		t.Fatalf("get reprise = %d %v", status, got)
	}

	// Play the reprise with different decisions, archive it, and complete.
	replayedSession := finishedSession(t, mgr, "inv_http_archive", 1)
	status, replayed := doJSON(t, http.MethodPost, srv.URL+"/hub/sessions/"+replayedSession+"/archive", nil)
	if status != http.StatusCreated {
		t.Fatalf("reprise archive = %d %v", status, replayed)
	}
	replayedID := replayed["summary"].(map[string]any)["id"].(string)

	status, done := doJSON(t, http.MethodPost, srv.URL+"/hub/reprises/"+repriseID+"/complete", map[string]any{
		"new_archive_id": replayedID, "new_session_id": replayedSession,
	})
	if status != http.StatusOK {
		t.Fatalf("complete = %d %v", status, done)
	}
	if done["status"] != "completed" {
		t.Fatalf("reprise status = %v", done["status"])
	}
	comparison := done["comparison"].(map[string]any)
	if comparison["divergence_point"].(float64) != 1 {
		t.Fatalf("divergence point = %v", comparison["divergence_point"])
	}
	if comparison["identical_config"] != true {
		t.Fatalf("comparison must report an identical configuration: %v", comparison)
	}
	if rounds, _ := comparison["rounds"].([]any); len(rounds) == 0 {
		t.Fatalf("comparison recorded no diverging rounds")
	}

	status, view := doJSON(t, http.MethodGet,
		srv.URL+"/hub/archives/"+archiveID+"/views/same_spec_comparison?compare_with="+replayedID, nil)
	if status != http.StatusOK {
		t.Fatalf("same_spec comparison = %d %v", status, view)
	}
	got := view["comparison"].(map[string]any)
	if got["divergence_point"].(float64) != 1 {
		t.Fatalf("view divergence = %v", got["divergence_point"])
	}
}

func TestArchiveHTTPRejectsBadInput(t *testing.T) {
	srv, mgr := archiveTestServer(t)
	defer srv.Close()

	// A live session cannot be archived.
	invitation := models.Invitation{
		Kind: "fluxspace.invitation.v1", InvitationID: "inv_live",
		FromAgentID: "agent_alpha", FromCompanionID: "comp_alpha", ToAgentID: "agent_beta",
		Proposals:        []models.Proposal{{Index: 1, Type: "deduction", Seats: 2, Minutes: 12}},
		SelectedProposal: 1,
		ExpiresAt:        1730000300000,
	}
	claims := []models.SeatClaim{
		{SeatID: "seat_0", OwnerID: "owner_alpha", AgentID: "agent_alpha", CompanionID: "comp_alpha", DisplayName: "Lin Shen"},
		{SeatID: "seat_1", OwnerID: "owner_beta", AgentID: "agent_beta", CompanionID: "comp_beta", DisplayName: "Su Wan"},
	}
	live, err := mgr.Create(session.CreateRequest{Invitation: invitation, Claims: claims, Seed: 5})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	status, body := doJSON(t, http.MethodPost, srv.URL+"/hub/sessions/"+live.SessionID+"/archive", nil)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("archiving a live session = %d %v", status, body)
	}

	// Unknown archive and unknown mode both fail loudly.
	if status, _ := doJSON(t, http.MethodGet, srv.URL+"/hub/archives/replay_nope", nil); status != http.StatusNotFound {
		t.Fatalf("unknown archive = %d", status)
	}
	if status, _ := doJSON(t, http.MethodGet, srv.URL+"/hub/archives/replay_nope/views/full", nil); status != http.StatusNotFound {
		t.Fatalf("unknown archive view = %d", status)
	}
}

func TestArchiveHTTPUnconfiguredStoreIsExplicit(t *testing.T) {
	mux := http.NewServeMux()
	RegisterArchives(mux, &Server{Manager: session.NewManager()})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	status, body := doJSON(t, http.MethodGet, srv.URL+"/hub/archives", nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured store = %d %v", status, body)
	}
	if code := nested(body, "error", "code"); code != "ARCHIVE_STORE_UNCONFIGURED" {
		t.Fatalf("code = %v", code)
	}
}

func nested(m map[string]any, keys ...string) any {
	var current any = m
	for _, key := range keys {
		asMap, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = asMap[key]
	}
	return current
}
