package session

import (
	"encoding/json"
	"strings"
	"testing"

	"fluxspace.ai/hub/models"
)

func testInvitation(id string) models.Invitation {
	return models.Invitation{
		Kind:             "fluxspace.invitation.v1",
		InvitationID:     id,
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
}

// two real owners and two clearly labelled official companions, matching the
// S1 exit criterion in IMPLEMENTATION_PLAN.md.
func testClaims() []models.SeatClaim {
	return []models.SeatClaim{
		{SeatID: "seat_0", OwnerID: "owner_alpha", AgentID: "agent_alpha", CompanionID: "comp_alpha", DisplayName: "Lin Shen"},
		{SeatID: "seat_1", OwnerID: "owner_beta", AgentID: "agent_beta", CompanionID: "comp_beta", DisplayName: "Su Wan"},
		{SeatID: "seat_2", OwnerID: "owner_official_a", AgentID: "agent_official_a", CompanionID: "comp_official_a", DisplayName: "Official Gu Yuan", Official: true},
		{SeatID: "seat_3", OwnerID: "owner_official_b", AgentID: "agent_official_b", CompanionID: "comp_official_b", DisplayName: "Official Shen Mo", Official: true},
	}
}

func createRequest(id string) CreateRequest {
	return CreateRequest{Invitation: testInvitation(id), Claims: testClaims(), Seed: 42}
}

func newManager() *Manager {
	return NewManagerWithClock(func() int64 { return 1700000000000 })
}

func joinAll(t *testing.T, m *Manager, sessionID string) {
	t.Helper()
	for _, claim := range testClaims() {
		if _, _, err := m.Join(sessionID, claim.SeatID, claim.OwnerID); err != nil {
			t.Fatalf("Join(%s): %v", claim.SeatID, err)
		}
	}
}

// runToEnd drives the real four-phase loop for every round via the public
// Manager API. It returns the final view.
func runToEnd(t *testing.T, m *Manager, sessionID string) models.SessionView {
	t.Helper()
	for step := 0; step < 20; step++ {
		view, err := m.State(sessionID)
		if err != nil {
			t.Fatalf("State: %v", err)
		}
		if view.Status == models.SessionFinished {
			return view
		}
		if view.Round > view.TotalRounds {
			t.Fatalf("round %d exceeded total %d without an ending", view.Round, view.TotalRounds)
		}
		if _, err := m.Advance(sessionID, models.PhaseDiscuss); err != nil {
			t.Fatalf("round %d advance discuss: %v", view.Round, err)
		}
		if err := m.PostDiscussion(sessionID, models.DiscussionMessage{
			SeatID: "seat_0", Text: "The ledger and the timetable disagree.", Channel: "public",
		}); err != nil {
			t.Fatalf("round %d discussion: %v", view.Round, err)
		}
		if _, err := m.Advance(sessionID, models.PhaseAct); err != nil {
			t.Fatalf("round %d advance act: %v", view.Round, err)
		}
		actionID := view.Skeleton.Rounds[view.Round-1].Actions[0].ID
		for _, claim := range testClaims() {
			if _, err := m.SubmitAction(sessionID, claim.SeatID, models.DecisionSubmission{
				ActionID: actionID, ReasoningSummary: "rule engine selected the top-scoring legal action",
				Confidence: 70, Source: "rule_engine",
			}); err != nil {
				t.Fatalf("round %d SubmitAction(%s): %v", view.Round, claim.SeatID, err)
			}
		}
	}
	t.Fatal("match did not finish within 20 rounds")
	return models.SessionView{}
}

func TestCreateValidatesInvitation(t *testing.T) {
	m := newManager()
	cases := []struct {
		name   string
		mutate func(*CreateRequest)
		code   string
	}{
		{"bad kind", func(r *CreateRequest) { r.Invitation.Kind = "other" }, "INVALID_INVITATION_KIND"},
		{"missing id", func(r *CreateRequest) { r.Invitation.InvitationID = "" }, "INVITATION_ID_REQUIRED"},
		{"no proposals", func(r *CreateRequest) { r.Invitation.Proposals = nil }, "PROPOSALS_REQUIRED"},
		{"bad selection", func(r *CreateRequest) { r.Invitation.SelectedProposal = 9 }, "INVALID_SELECTED_PROPOSAL"},
		{"one seat", func(r *CreateRequest) { r.Claims = r.Claims[:1] }, "INVALID_SEAT_COUNT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := createRequest("inv_" + tc.name)
			tc.mutate(&req)
			_, err := m.Create(req)
			if err == nil {
				t.Fatal("expected rejection")
			}
			domain, ok := AsError(err)
			if !ok || domain.Code != tc.code {
				t.Fatalf("got %v, want code %s", err, tc.code)
			}
		})
	}
}

func TestCreateEnforcesSeatUniqueness(t *testing.T) {
	m := newManager()
	req := createRequest("inv_dup_owner")
	req.Claims[1].OwnerID = req.Claims[0].OwnerID
	_, err := m.Create(req)
	domain, ok := AsError(err)
	if !ok || domain.Code != "OWNER_SEAT_CONFLICT" {
		t.Fatalf("got %v, want OWNER_SEAT_CONFLICT", err)
	}
}

func TestCreateEnforcesOneLiveMatchPerOwner(t *testing.T) {
	m := newManager()
	if _, err := m.Create(createRequest("inv_first")); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := m.Create(createRequest("inv_second"))
	domain, ok := AsError(err)
	if !ok || domain.Code != "OWNER_HAS_LIVE_MATCH" {
		t.Fatalf("got %v, want OWNER_HAS_LIVE_MATCH", err)
	}
}

func TestOwnerReleasedAfterMatchFinishes(t *testing.T) {
	m := newManager()
	view, err := m.Create(createRequest("inv_release"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	joinAll(t, m, view.SessionID)
	final := runToEnd(t, m, view.SessionID)
	if final.Status != models.SessionFinished {
		t.Fatalf("status = %s", final.Status)
	}
	if _, err := m.Create(createRequest("inv_after_release")); err != nil {
		t.Fatalf("owner was not released: %v", err)
	}
}

func TestJoinRejectsOwnerMismatchAndDuplicate(t *testing.T) {
	m := newManager()
	view, _ := m.Create(createRequest("inv_join"))
	if _, _, err := m.Join(view.SessionID, "seat_0", "impostor"); err == nil || !strings.Contains(err.Error(), "OWNER_MISMATCH") {
		t.Fatalf("expected OWNER_MISMATCH, got %v", err)
	}
	if _, _, err := m.Join(view.SessionID, "seat_0", "owner_alpha"); err != nil {
		t.Fatalf("valid join: %v", err)
	}
	if _, _, err := m.Join(view.SessionID, "seat_0", "owner_alpha"); err == nil || !strings.Contains(err.Error(), "SEAT_ALREADY_JOINED") {
		t.Fatalf("expected SEAT_ALREADY_JOINED, got %v", err)
	}
}

func TestAdvanceRejectsIllegalTransitions(t *testing.T) {
	m := newManager()
	view, _ := m.Create(createRequest("inv_phase"))
	if _, err := m.Advance(view.SessionID, models.PhaseDiscuss); err == nil || !strings.Contains(err.Error(), "SESSION_NOT_LIVE") {
		t.Fatalf("expected SESSION_NOT_LIVE before all joins, got %v", err)
	}
	joinAll(t, m, view.SessionID)
	if _, err := m.Advance(view.SessionID, models.PhaseAct); err == nil || !strings.Contains(err.Error(), "INVALID_PHASE_TRANSITION") {
		t.Fatalf("expected INVALID_PHASE_TRANSITION, got %v", err)
	}
	if _, err := m.Advance(view.SessionID, models.PhaseDiscuss); err != nil {
		t.Fatalf("observe -> discuss: %v", err)
	}
	if _, err := m.Advance(view.SessionID, models.PhaseAct); err != nil {
		t.Fatalf("discuss -> act: %v", err)
	}
	if _, err := m.Advance(view.SessionID, models.PhaseObserve); err == nil || !strings.Contains(err.Error(), "AWAITING_DECISIONS") {
		t.Fatalf("expected AWAITING_DECISIONS, got %v", err)
	}
}

func TestDiscussionBoundsAndVisibility(t *testing.T) {
	m := newManager()
	view, _ := m.Create(createRequest("inv_talk"))
	joinAll(t, m, view.SessionID)
	if _, err := m.Advance(view.SessionID, models.PhaseDiscuss); err != nil {
		t.Fatal(err)
	}
	if err := m.PostDiscussion(view.SessionID, models.DiscussionMessage{
		SeatID: "seat_0", Text: strings.Repeat("x", 201), Channel: "public",
	}); err == nil || !strings.Contains(err.Error(), "INVALID_DISCUSSION_LENGTH") {
		t.Fatalf("expected length rejection, got %v", err)
	}
	if err := m.PostDiscussion(view.SessionID, models.DiscussionMessage{
		SeatID: "seat_0", Text: "private note", Channel: "private",
	}); err != nil {
		t.Fatalf("private line: %v", err)
	}
	if err := m.PostDiscussion(view.SessionID, models.DiscussionMessage{
		SeatID: "seat_0", Text: "second", Channel: "public",
	}); err != nil {
		t.Fatalf("second line: %v", err)
	}
	if err := m.PostDiscussion(view.SessionID, models.DiscussionMessage{
		SeatID: "seat_0", Text: "third", Channel: "public",
	}); err == nil || !strings.Contains(err.Error(), "DISCUSSION_QUOTA_EXCEEDED") {
		t.Fatalf("expected quota rejection, got %v", err)
	}

	_, _, err := m.EventsSince(view.SessionID, "seat_1", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	other, _, _ := m.EventsSince(view.SessionID, "seat_1", false, 0)
	for _, ev := range other {
		if ev.Visibility == models.VisibilityPrivate {
			t.Fatalf("private event leaked to seat_1: %+v", ev)
		}
	}
	owner, _, _ := m.EventsSince(view.SessionID, "seat_0", false, 0)
	found := false
	for _, ev := range owner {
		if ev.Type == models.EventDiscussion && ev.Visibility == models.VisibilityPrivate {
			found = true
		}
	}
	if !found {
		t.Fatal("private discussion missing from its intended seat")
	}
	all, _, _ := m.EventsSince(view.SessionID, "", true, 0)
	if len(all) <= len(owner) {
		t.Fatalf("omniscient stream (%d) must be a superset of participant (%d)", len(all), len(owner))
	}
}

func TestSubmitActionRejectsIllegalAndDuplicateDecisions(t *testing.T) {
	m := newManager()
	view, _ := m.Create(createRequest("inv_actions"))
	joinAll(t, m, view.SessionID)
	if _, err := m.Advance(view.SessionID, models.PhaseDiscuss); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Advance(view.SessionID, models.PhaseAct); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SubmitAction(view.SessionID, "seat_0", models.DecisionSubmission{
		ActionID: "not_in_pool", Confidence: 50,
	}); err == nil || !strings.Contains(err.Error(), "ILLEGAL_ACTION_ID") {
		t.Fatalf("expected ILLEGAL_ACTION_ID, got %v", err)
	}
	legal := view.Skeleton.Rounds[0].Actions[0].ID
	if _, err := m.SubmitAction(view.SessionID, "seat_0", models.DecisionSubmission{
		ActionID: legal, Confidence: 101,
	}); err == nil || !strings.Contains(err.Error(), "INVALID_CONFIDENCE") {
		t.Fatalf("expected INVALID_CONFIDENCE, got %v", err)
	}
	if _, err := m.SubmitAction(view.SessionID, "seat_0", models.DecisionSubmission{
		ActionID: legal, Confidence: 70,
	}); err != nil {
		t.Fatalf("valid submission: %v", err)
	}
	if _, err := m.SubmitAction(view.SessionID, "seat_0", models.DecisionSubmission{
		ActionID: legal, Confidence: 70,
	}); err == nil || !strings.Contains(err.Error(), "DECISION_ALREADY_SUBMITTED") {
		t.Fatalf("expected DECISION_ALREADY_SUBMITTED, got %v", err)
	}
}

func TestFiveRoundMatchProducesNonNeutralEnding(t *testing.T) {
	m := newManager()
	view, err := m.Create(createRequest("inv_full_match"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if view.TotalRounds != 5 {
		t.Fatalf("total rounds = %d, want 5", view.TotalRounds)
	}
	if len(view.OfficialSeatIDs) != 2 {
		t.Fatalf("official seats = %v, want two labelled officials", view.OfficialSeatIDs)
	}
	joinAll(t, m, view.SessionID)
	final := runToEnd(t, m, view.SessionID)
	if final.Status != models.SessionFinished {
		t.Fatalf("status = %s, want finished", final.Status)
	}
	switch final.Outcome {
	case "win", "lose", "partial", "regret":
	default:
		t.Fatalf("outcome %q is neutral or unknown; rounds_out must resolve to partial/regret", final.Outcome)
	}

	events, latest, err := m.EventsSince(view.SessionID, "", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if latest != final.LastEventID {
		t.Fatalf("latest %d != last_event_id %d", latest, final.LastEventID)
	}
	seen := map[string]int{}
	for i, ev := range events {
		if i > 0 && ev.ID <= events[i-1].ID {
			t.Fatalf("event IDs not monotonic at %d", i)
		}
		if ev.SessionID != view.SessionID {
			t.Fatalf("event %d has wrong session id", ev.ID)
		}
		if len(ev.Payload) == 0 || len(ev.StateSnapshot) == 0 {
			t.Fatalf("event %d missing payload/state_snapshot", ev.ID)
		}
		seen[ev.Type]++
	}
	for _, required := range []string{
		models.EventMatchStart, models.EventRoundBegin, models.EventInformationReveal,
		models.EventDiscussion, models.EventDecision, models.EventResolution, models.EventMatchEnd,
	} {
		if seen[required] == 0 {
			t.Errorf("event type %q never published", required)
		}
	}
	if seen[models.EventDecision] != 20 {
		t.Errorf("decision events = %d, want 4 seats x 5 rounds = 20", seen[models.EventDecision])
	}
	if seen[models.EventResolution] != 5 {
		t.Errorf("resolution events = %d, want 5", seen[models.EventResolution])
	}
}

func TestLastEventIDRecoveryAndPrivateFiltering(t *testing.T) {
	m := newManager()
	view, _ := m.Create(createRequest("inv_recovery"))
	joinAll(t, m, view.SessionID)
	_ = runToEnd(t, m, view.SessionID)

	all, _, err := m.EventsSince(view.SessionID, "", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 10 {
		t.Fatalf("only %d events", len(all))
	}
	cutoff := all[len(all)/2].ID
	after, _, err := m.EventsSince(view.SessionID, "", true, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) == 0 || after[0].ID <= cutoff {
		t.Fatalf("recovery from %d returned %d events starting at %d", cutoff, len(after), after[0].ID)
	}
	participant, _, _ := m.EventsSince(view.SessionID, "seat_1", false, 0)
	for _, ev := range participant {
		if ev.Visibility == models.VisibilityOmniscient {
			t.Fatalf("omniscient-only event %d leaked to participant", ev.ID)
		}
	}
}

func TestMatchIsDeterministicFromInvitationAndSeed(t *testing.T) {
	run := func() (string, []byte) {
		m := newManager()
		view, err := m.Create(createRequest("inv_determinism"))
		if err != nil {
			t.Fatal(err)
		}
		joinAll(t, m, view.SessionID)
		final := runToEnd(t, m, view.SessionID)
		events, _, _ := m.EventsSince(view.SessionID, "", true, 0)
		encoded, err := json.Marshal(events)
		if err != nil {
			t.Fatal(err)
		}
		return final.Outcome, encoded
	}
	firstOutcome, firstEvents := run()
	secondOutcome, secondEvents := run()
	if firstOutcome != secondOutcome {
		t.Fatalf("outcome differs: %s vs %s", firstOutcome, secondOutcome)
	}
	if string(firstEvents) != string(secondEvents) {
		t.Fatal("event stream differs for identical invitation and seed")
	}
}

func TestSnapshotIsDetached(t *testing.T) {
	m := newManager()
	view, _ := m.Create(createRequest("inv_snapshot"))
	joinAll(t, m, view.SessionID)
	snapshot, err := m.Snapshot(view.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Metrics["truth"] = models.Metric{Key: "truth", Value: 9999}
	snapshot.Seats[0].DisplayName = "mutated"
	snapshot.Skeleton.Title = "mutated"
	after, _ := m.State(view.SessionID)
	if after.Metrics["truth"].Value == 9999 || after.Seats[0].DisplayName == "mutated" || after.Skeleton.Title == "mutated" {
		t.Fatal("snapshot aliases manager state")
	}
}

// TestSeatStreamTokenIsPerSeatAndUnguessable covers ADR-0009 §2.3: a seat's
// private stream used to be readable from the seat id alone.
func TestSeatStreamTokenIsPerSeatAndUnguessable(t *testing.T) {
	m := newManager()
	view, _ := m.Create(createRequest("inv_seat_token"))
	if err := m.VerifySeatToken(view.SessionID, "seat_0", "anything"); err == nil {
		t.Fatal("an unjoined seat accepted a stream token")
	}
	var tokenA string
	{
		var err error
		if _, tokenA, err = m.Join(view.SessionID, "seat_0", "owner_alpha"); err != nil {
			t.Fatalf("join seat_0: %v", err)
		}
		if len(tokenA) < 32 {
			t.Fatalf("token %q is too short to be unguessable", tokenA)
		}
	}
	_, tokenB, err := m.Join(view.SessionID, "seat_1", "owner_beta")
	if err != nil {
		t.Fatalf("join seat_1: %v", err)
	}

	if err := m.VerifySeatToken(view.SessionID, "seat_0", tokenA); err != nil {
		t.Fatalf("own token rejected: %v", err)
	}
	// A token from another seat of the same session must not open this stream.
	if err := m.VerifySeatToken(view.SessionID, "seat_0", tokenB); err == nil {
		t.Fatal("seat_1's token opened seat_0's stream")
	}
	if err := m.VerifySeatToken(view.SessionID, "seat_0", ""); err == nil {
		t.Fatal("an empty token was accepted")
	}
}
