package archive

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"fluxspace.ai/hub/apperr"
	"fluxspace.ai/hub/models"
	"fluxspace.ai/hub/session"
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

func testClaims() []models.SeatClaim {
	return []models.SeatClaim{
		{SeatID: "seat_0", OwnerID: "owner_alpha", AgentID: "agent_alpha", CompanionID: "comp_alpha", DisplayName: "Lin Shen"},
		{SeatID: "seat_1", OwnerID: "owner_beta", AgentID: "agent_beta", CompanionID: "comp_beta", DisplayName: "Su Wan"},
		{SeatID: "seat_2", OwnerID: "owner_official_a", AgentID: "agent_official_a", CompanionID: "comp_official_a", DisplayName: "Official Gu Yuan", Official: true},
		{SeatID: "seat_3", OwnerID: "owner_official_b", AgentID: "agent_official_b", CompanionID: "comp_official_b", DisplayName: "Official Shen Mo", Official: true},
	}
}

// fixture drives a real four-phase match through the session Manager and
// returns the finished view plus its retained event stream. actionOffset lets a
// test pick a different legal action so two runs share a skeleton but diverge.
func fixture(t *testing.T, invitationID string, actionOffset int) (models.SessionView, []models.Event) {
	return fixtureWithSeed(t, invitationID, actionOffset, 42)
}

// fixtureWithSeed is fixture with an explicit seed. The seed, not the
// invitation id, decides the skeleton, so tests that need a different skeleton
// must vary it here.
func fixtureWithSeed(t *testing.T, invitationID string, actionOffset int, seed int64) (models.SessionView, []models.Event) {
	t.Helper()
	now := int64(1700000000000)
	m := session.NewManagerWithClock(func() int64 {
		now += 250
		return now
	})
	view, err := m.Create(session.CreateRequest{
		Invitation: testInvitation(invitationID), Claims: testClaims(), Seed: seed,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, claim := range testClaims() {
		if _, err := m.Join(view.SessionID, claim.SeatID, claim.OwnerID); err != nil {
			t.Fatalf("Join(%s): %v", claim.SeatID, err)
		}
	}

	for step := 0; step < 20; step++ {
		current, err := m.State(view.SessionID)
		if err != nil {
			t.Fatalf("State: %v", err)
		}
		if current.Status == models.SessionFinished {
			events, _, err := m.EventsSince(view.SessionID, "", true, 0)
			if err != nil {
				t.Fatalf("EventsSince: %v", err)
			}
			return current, events
		}
		if _, err := m.Advance(view.SessionID, models.PhaseDiscuss); err != nil {
			t.Fatalf("round %d discuss: %v", current.Round, err)
		}
		if err := m.PostDiscussion(view.SessionID, models.DiscussionMessage{
			SeatID: "seat_0", Text: "The ledger and the timetable disagree.", Channel: "public",
		}); err != nil {
			t.Fatalf("round %d public discussion: %v", current.Round, err)
		}
		// One private line from seat_1, so the single-character mode has
		// something that must be filtered out for seat_0.
		if err := m.PostDiscussion(view.SessionID, models.DiscussionMessage{
			SeatID: "seat_1", Text: "I do not trust the third clause.", Channel: "private",
		}); err != nil {
			t.Fatalf("round %d private discussion: %v", current.Round, err)
		}
		if _, err := m.Advance(view.SessionID, models.PhaseAct); err != nil {
			t.Fatalf("round %d act: %v", current.Round, err)
		}
		actions := current.Skeleton.Rounds[current.Round-1].Actions
		actionID := actions[actionOffset%len(actions)].ID
		for _, claim := range testClaims() {
			if _, err := m.SubmitAction(view.SessionID, claim.SeatID, models.DecisionSubmission{
				ActionID: actionID, ReasoningSummary: "rule engine picked the top legal action",
				Confidence: 70, Source: "rule_engine",
			}); err != nil {
				t.Fatalf("round %d SubmitAction(%s): %v", current.Round, claim.SeatID, err)
			}
		}
	}
	t.Fatal("match did not finish within 20 rounds")
	return models.SessionView{}, nil
}

func buildArchive(t *testing.T, invitationID string, actionOffset int) Archive {
	t.Helper()
	return buildArchiveWithSeed(t, invitationID, actionOffset, 42)
}

func buildArchiveWithSeed(t *testing.T, invitationID string, actionOffset int, seed int64) Archive {
	t.Helper()
	view, events := fixtureWithSeed(t, invitationID, actionOffset, seed)
	a, err := Build(view, events, Options{Now: 1730000400000})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return a
}

func TestBuildRefusesUnfinishedSession(t *testing.T) {
	m := session.NewManagerWithClock(func() int64 { return 1700000000000 })
	view, err := m.Create(session.CreateRequest{
		Invitation: testInvitation("inv_unfinished"), Claims: testClaims(), Seed: 42,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	events, _, err := m.EventsSince(view.SessionID, "", true, 0)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	if _, err := Build(view, events, Options{}); err == nil ||
		!strings.Contains(err.Error(), "finished session") {
		t.Fatalf("archiving a live session must fail, got %v", err)
	}
}

func TestBuildCapturesVerifiableArchive(t *testing.T) {
	a := buildArchive(t, "inv_archive", 0)

	if err := a.Verify(); err != nil {
		t.Fatalf("Verify on a fresh archive: %v", err)
	}
	if a.Kind != Kind || a.Version != Version {
		t.Fatalf("kind/version = %q/%q", a.Kind, a.Version)
	}
	if !strings.HasPrefix(a.Integrity.ContentHash, "sha256:") ||
		!strings.HasPrefix(a.Integrity.SkeletonHash, "sha256:") {
		t.Fatalf("hashes are not SHA-256: %+v", a.Integrity)
	}
	if a.Integrity.EventCount == 0 || a.Integrity.EventCount != len(a.Events) {
		t.Fatalf("event count %d vs %d events", a.Integrity.EventCount, len(a.Events))
	}
	if !strings.HasPrefix(a.ID, "replay_") {
		t.Fatalf("id = %q", a.ID)
	}
	// The Hub does not know persona ids or model versions, so the archive must
	// say so rather than leave a convincing blank.
	if a.Integrity.Complete {
		t.Fatalf("archive claims completeness without persona_id/model_version")
	}
	joined := strings.Join(a.Integrity.MissingFields, ",")
	if !strings.Contains(joined, "model_version") || !strings.Contains(joined, "persona_id") {
		t.Fatalf("missing fields = %v", a.Integrity.MissingFields)
	}
	// Official backfills have no owner and therefore never sign consent.
	if got := a.SigningParticipants(); len(got) != 2 {
		t.Fatalf("signing participants = %v, want 2 real owners", got)
	}
	if a.Settlement.Outcome == "" || a.Settlement.RoundsPlayed == 0 {
		t.Fatalf("settlement = %+v", a.Settlement)
	}
}

func TestClientSuppliedSeatsCompleteTheArchive(t *testing.T) {
	view, events := fixture(t, "inv_complete", 0)
	seats := []SeatConfig{
		{SeatID: "seat_0", PersonaID: "lin-shen", ModelVersion: "claude-sonnet-4-20250514",
			PersonaSnapshot: json.RawMessage(`{"risk":50,"logic":70,"empathy":60,"coop":55,"aggression":30,"intuition":45}`)},
		{SeatID: "seat_1", PersonaID: "su-wan", ModelVersion: "claude-sonnet-4-20250514",
			PersonaSnapshot: json.RawMessage(`{"risk":40,"logic":65,"empathy":70,"coop":60,"aggression":25,"intuition":55}`)},
		{SeatID: "seat_2", PersonaID: "gu-yuan", ModelVersion: "official-v1",
			PersonaSnapshot: json.RawMessage(`{"risk":45,"logic":60,"empathy":50,"coop":50,"aggression":35,"intuition":50}`)},
		{SeatID: "seat_3", PersonaID: "shen-mo", ModelVersion: "official-v1",
			PersonaSnapshot: json.RawMessage(`{"risk":55,"logic":55,"empathy":45,"coop":45,"aggression":45,"intuition":50}`)},
	}
	a, err := Build(view, events, Options{Now: 1, Seats: seats})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !a.Integrity.Complete || len(a.Integrity.MissingFields) != 0 {
		t.Fatalf("archive should be complete: %+v", a.Integrity)
	}
	if a.Config.Seats[0].PersonaSource != "client_supplied" {
		t.Fatalf("persona source = %q", a.Config.Seats[0].PersonaSource)
	}
	if a.Config.Seats[0].PersonaID != "lin-shen" || a.Config.Seats[0].ModelVersion == "" {
		t.Fatalf("seat 0 = %+v", a.Config.Seats[0])
	}
	if err := a.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	base := buildArchive(t, "inv_tamper", 0)

	cases := []struct {
		name   string
		mutate func(*Archive)
		want   string
	}{
		{"event payload", func(a *Archive) {
			a.Events[1].Payload = json.RawMessage(`{"tampered":true}`)
		}, "content hash mismatch"},
		{"settlement outcome", func(a *Archive) { a.Settlement.Outcome += "_tampered" }, "content hash mismatch"},
		{"dropped event", func(a *Archive) { a.Events = a.Events[:len(a.Events)-1] }, "content hash mismatch"},
		{"recorded count", func(a *Archive) { a.Integrity.EventCount++ }, "event count mismatch"},
		{"recorded skeleton hash", func(a *Archive) { a.Integrity.SkeletonHash = "sha256:00" }, "skeleton hash mismatch"},
		{"unverified flag", func(a *Archive) { a.Integrity.Verified = false }, "marked unverified"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broken := base
			broken.Events = deepCopyEvents(base.Events)
			skeleton := *base.Config.Skeleton
			broken.Config = base.Config
			broken.Config.Skeleton = &skeleton
			tc.mutate(&broken)
			// The recorded hashes stay from the original archive: that is
			// exactly what a tampered archive looks like on disk.
			err := broken.Verify()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Verify() = %v, want %q", err, tc.want)
			}
		})
	}
}

func deepCopyEvents(in []models.Event) []models.Event {
	out := make([]models.Event, len(in))
	copy(out, in)
	for i := range out {
		out[i].Payload = append(json.RawMessage(nil), in[i].Payload...)
		out[i].StateSnapshot = append(json.RawMessage(nil), in[i].StateSnapshot...)
	}
	return out
}

func TestLoadRejectsTamperedJSON(t *testing.T) {
	a := buildArchive(t, "inv_load", 0)
	data, err := a.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := Load(data); err != nil {
		t.Fatalf("Load on intact bytes: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	settlement := doc["settlement"].(map[string]any)
	settlement["outcome"] = "win"
	tampered, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if _, err := Load(tampered); err == nil || !strings.Contains(err.Error(), "content hash mismatch") {
		t.Fatalf("Load on tampered bytes must fail, got %v", err)
	}
}

func TestProjectFullAndAccelerated(t *testing.T) {
	a := buildArchive(t, "inv_timeline", 0)

	full, err := a.Project(Query{Mode: ModeFull})
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	if len(full.Events) != len(a.Events) {
		t.Fatalf("full view has %d of %d events", len(full.Events), len(a.Events))
	}
	if full.Events[0].Type != models.EventMatchStart {
		t.Fatalf("first event = %s", full.Events[0].Type)
	}
	if last := full.Events[len(full.Events)-1]; last.Type != models.EventMatchEnd {
		t.Fatalf("last event = %s", last.Type)
	}

	fast, err := a.Project(Query{Mode: ModeAccelerated, Speed: 2})
	if err != nil {
		t.Fatalf("accelerated: %v", err)
	}
	if len(fast.Events) != len(full.Events) {
		t.Fatalf("accelerated dropped events: %d vs %d", len(fast.Events), len(full.Events))
	}
	if fast.Speed != 2 || fast.DurationMS*2 > full.DurationMS+1 {
		t.Fatalf("speed %d, duration %d vs full %d", fast.Speed, fast.DurationMS, full.DurationMS)
	}
	if _, err := a.Project(Query{Mode: ModeAccelerated, Speed: 3}); err == nil {
		t.Fatal("speed 3 must be rejected, not silently rounded")
	}
}

func TestSingleCharacterHidesOtherSeatsPrivateEvents(t *testing.T) {
	a := buildArchive(t, "inv_character", 0)

	seat0, err := a.Project(Query{Mode: ModeSingleCharacter, SeatID: "seat_0"})
	if err != nil {
		t.Fatalf("seat_0 view: %v", err)
	}
	seat1, err := a.Project(Query{Mode: ModeSingleCharacter, SeatID: "seat_1"})
	if err != nil {
		t.Fatalf("seat_1 view: %v", err)
	}
	if len(seat1.Events) <= len(seat0.Events) {
		t.Fatalf("seat_1 should see its own private lines (%d vs %d)", len(seat1.Events), len(seat0.Events))
	}
	for _, ev := range seat0.Events {
		if ev.Visibility == models.VisibilityPrivate && ev.SeatID != "seat_0" {
			t.Fatalf("seat_0 view leaked a private event from %s", ev.SeatID)
		}
	}
	if _, err := a.Project(Query{Mode: ModeSingleCharacter}); err == nil {
		t.Fatal("single_character without a seat must fail")
	}
	if _, err := a.Project(Query{Mode: ModeSingleCharacter, SeatID: "seat_9"}); err == nil {
		t.Fatal("unknown seat must fail")
	}
}

func TestOmniscientComparisonLaysDecisionsSideBySide(t *testing.T) {
	a := buildArchive(t, "inv_omniscient", 0)
	view, err := a.Project(Query{Mode: ModeOmniscientComparison})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(view.Rounds) != a.Settlement.RoundsPlayed {
		t.Fatalf("rounds = %d, want %d", len(view.Rounds), a.Settlement.RoundsPlayed)
	}
	for _, round := range view.Rounds {
		if len(round.Decisions) != 4 {
			t.Fatalf("round %d has %d decisions, want 4", round.Round, len(round.Decisions))
		}
		for i, d := range round.Decisions {
			if d.SeatID != []string{"seat_0", "seat_1", "seat_2", "seat_3"}[i] {
				t.Fatalf("round %d decision %d is %s, seats are out of order", round.Round, i, d.SeatID)
			}
			if d.ActionID == "" {
				t.Fatalf("round %d seat %s has no action", round.Round, d.SeatID)
			}
		}
	}
}

func TestKeyMomentsAreDerivedAndDeterministic(t *testing.T) {
	a := buildArchive(t, "inv_moments", 0)
	first, err := a.Project(Query{Mode: ModeKeyMoments, MomentLimit: 4})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	second, err := a.Project(Query{Mode: ModeKeyMoments, MomentLimit: 4})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(first.Moments) != len(second.Moments) {
		t.Fatalf("moments are not deterministic: %d vs %d", len(first.Moments), len(second.Moments))
	}
	for i := range first.Moments {
		if first.Moments[i] != second.Moments[i] {
			t.Fatalf("moment %d differs between runs", i)
		}
	}
	if len(first.Moments) == 0 {
		t.Fatal("no moments marked")
	}
	if last := first.Moments[len(first.Moments)-1]; last.Kind != "outcome" {
		t.Fatalf("last moment is %q, want the terminal outcome", last.Kind)
	}
	for _, m := range first.Moments {
		if m.Reason == "" || m.EventID == 0 {
			t.Fatalf("moment lacks evidence: %+v", m)
		}
	}
}

func TestAllSixModesAreProjectable(t *testing.T) {
	modes := Modes()
	if len(modes) != 6 {
		t.Fatalf("Modes() = %v, want six", modes)
	}
	a := buildArchive(t, "inv_modes", 0)
	b := buildArchive(t, "inv_modes_alt", 0)
	for _, mode := range modes {
		q := Query{Mode: mode}
		switch mode {
		case ModeAccelerated:
			q.Speed = 4
		case ModeSingleCharacter:
			q.SeatID = "seat_0"
		case ModeSameSpecComparison:
			q.CompareWith = &b
		}
		if _, err := a.Project(q); err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
	}
	if _, err := a.Project(Query{Mode: "video"}); err == nil {
		t.Fatal("unknown mode must fail")
	}
}

func TestConsentGateAllPolicy(t *testing.T) {
	store := NewStore()
	a := buildArchive(t, "inv_consent_all", 0)
	if _, err := store.Put(a); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Nothing granted yet: publication must be refused, naming who is missing.
	if _, err := store.Publish(a.ID, 1); err == nil {
		t.Fatal("publishing without consent must fail")
	} else if e, ok := apperr.AsError(err); !ok || e.Code != "REPLAY_CONSENT_INCOMPLETE" || e.Status != http.StatusForbidden {
		t.Fatalf("error = %v", err)
	}

	// One of two owners: still refused under "all".
	if err := store.Ledger().Record(a.ID, "comp_alpha", "owner_alpha", true, 2); err != nil {
		t.Fatalf("Record: %v", err)
	}
	decision, err := store.Publish(a.ID, 2)
	if err == nil {
		t.Fatal("half the participants is not consent under the all policy")
	}
	if len(decision.Missing) != 1 || decision.Missing[0] != "comp_beta" {
		t.Fatalf("missing = %v", decision.Missing)
	}

	// Both owners: published.
	if err := store.Ledger().Record(a.ID, "comp_beta", "owner_beta", true, 3); err != nil {
		t.Fatalf("Record: %v", err)
	}
	decision, err = store.Publish(a.ID, 3)
	if err != nil {
		t.Fatalf("Publish with full consent: %v", err)
	}
	if !decision.Public || len(decision.Missing) != 0 {
		t.Fatalf("decision = %+v", decision)
	}
	rec, err := store.Get(a.ID)
	if err != nil || !rec.Published {
		t.Fatalf("record not published: %+v err=%v", rec, err)
	}
	// Publishing must not touch the immutable hashes.
	if err := rec.Archive.Verify(); err != nil {
		t.Fatalf("publish mutated the archive: %v", err)
	}
}

func TestConsentPolicyMajorityAndNone(t *testing.T) {
	store := NewStore()
	// Majority is evaluated over two real participants, so "more than half"
	// means both of them: a two-seat match must not be publishable on one vote.
	view, events := fixture(t, "inv_consent_majority", 0)
	view.SocialContract.ReplayConsent = PolicyMajority
	built, err := Build(view, events, Options{Now: 1})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := store.Put(built); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Ledger().Record(built.ID, "comp_alpha", "owner_alpha", true, 1); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// Two participants: "more than half" means both, so one grant is not enough.
	if _, err := store.Publish(built.ID, 1); err == nil {
		t.Fatal("one of two is not a majority")
	}
	if err := store.Ledger().Record(built.ID, "comp_beta", "owner_beta", true, 2); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := store.Publish(built.ID, 2); err != nil {
		t.Fatalf("Publish with both: %v", err)
	}

	// Policy "none" can never publish, even with unanimous consent.
	viewNone, eventsNone := fixture(t, "inv_consent_none", 0)
	viewNone.SocialContract.ReplayConsent = PolicyNone
	none, err := Build(viewNone, eventsNone, Options{Now: 1})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := store.Put(none); err != nil {
		t.Fatalf("Put: %v", err)
	}
	_ = store.Ledger().Record(none.ID, "comp_alpha", "owner_alpha", true, 1)
	_ = store.Ledger().Record(none.ID, "comp_beta", "owner_beta", true, 2)
	if _, err := store.Publish(none.ID, 3); err == nil {
		t.Fatal("policy none must never publish")
	}
}

func TestEmptyPolicyFailsClosed(t *testing.T) {
	view, events := fixture(t, "inv_consent_empty", 0)
	view.SocialContract.ReplayConsent = ""
	built, err := Build(view, events, Options{Now: 1})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	store := NewStore()
	if _, err := store.Put(built); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := store.Publish(built.ID, 1); err == nil {
		t.Fatal("an unspecified policy must default to the strictest, not to open")
	}
}

func TestRepriseRequiresEveryParticipantAndFindsDivergence(t *testing.T) {
	store := NewStore()
	original := buildArchive(t, "inv_reprise_a", 0)
	replayed := buildArchive(t, "inv_reprise_a", 1)
	other := buildArchiveWithSeed(t, "inv_reprise_other", 0, 7777)

	if original.SkeletonHash() != replayed.SkeletonHash() {
		t.Fatalf("same invitation and seed must share a skeleton hash")
	}
	for _, a := range []Archive{original, replayed, other} {
		if _, err := store.Put(a); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	if _, err := store.CreateReprise(original.ID, 1); err == nil {
		t.Fatal("a reprise without every owner's consent must be refused")
	} else if e, ok := apperr.AsError(err); !ok || e.Code != "REPRISE_CONSENT_INCOMPLETE" {
		t.Fatalf("error = %v", err)
	}

	for _, companion := range []string{"comp_alpha", "comp_beta"} {
		if err := store.Ledger().Record(original.ID, companion, "owner_"+companion, true, 2); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	reprise, err := store.CreateReprise(original.ID, 2)
	if err != nil {
		t.Fatalf("CreateReprise: %v", err)
	}
	if reprise.Status != RepriseStatusAuthorized || reprise.Type != RepriseSameSpec {
		t.Fatalf("reprise = %+v", reprise)
	}

	// A different skeleton is not a same-spec reprise.
	if _, err := store.CompleteReprise(reprise.ID, other.ID, "sess_other"); err == nil {
		t.Fatal("comparing against a different skeleton must fail")
	} else if e, ok := apperr.AsError(err); !ok || e.Code != "NOT_A_SAME_SPEC_REPRISE" {
		t.Fatalf("error = %v", err)
	}

	done, err := store.CompleteReprise(reprise.ID, replayed.ID, "sess_replayed")
	if err != nil {
		t.Fatalf("CompleteReprise: %v", err)
	}
	if done.Status != RepriseStatusCompleted {
		t.Fatalf("status = %s", done.Status)
	}
	cmp := done.Comparison
	if cmp == nil || !cmp.IdenticalConfig {
		t.Fatalf("comparison = %+v", cmp)
	}
	if cmp.DivergencePoint != 1 {
		t.Fatalf("divergence point = %d, want round 1", cmp.DivergencePoint)
	}
	if len(cmp.Rounds) == 0 || len(cmp.Rounds[0].Differences) == 0 {
		t.Fatalf("no per-round differences recorded: %+v", cmp.Rounds)
	}
	if got := cmp.Rounds[0].Differences[0]; got.OriginalAction == "" || got.RepriseAction == "" ||
		got.OriginalAction == got.RepriseAction {
		t.Fatalf("difference is not a real difference: %+v", got)
	}
}

func TestStoreRejectsArchiveIDConflict(t *testing.T) {
	store := NewStore()
	// A put that fails verification is rejected earlier as ARCHIVE_INVALID, so
	// the id-conflict branch is only reachable with two archives that both
	// verify but were captured from different sessions under one id.
	first := buildArchiveWithSeed(t, "inv_conflict_a", 0, 42)
	second := buildArchiveWithSeed(t, "inv_conflict_b", 0, 7777)
	if first.ContentHash() == second.ContentHash() {
		t.Fatal("test setup: two seeds produced the same archive")
	}
	first.ID = "replay_shared"
	second.ID = "replay_shared"
	if err := first.Verify(); err != nil {
		t.Fatalf("first archive should still verify: %v", err)
	}
	if err := second.Verify(); err != nil {
		t.Fatalf("second archive should still verify: %v", err)
	}
	if _, err := store.Put(first); err != nil {
		t.Fatalf("Put first: %v", err)
	}
	if _, err := store.Put(second); err == nil {
		t.Fatal("same id with different content must be rejected")
	} else if e, ok := apperr.AsError(err); !ok || e.Code != "ARCHIVE_ID_CONFLICT" {
		t.Fatalf("error = %v", err)
	}
	if _, err := store.Put(first); err != nil {
		t.Fatalf("re-putting identical content must be idempotent: %v", err)
	}

	tampered := buildArchive(t, "inv_conflict_c", 0)
	tampered.Settlement.Outcome += "_tampered"
	if _, err := store.Put(tampered); err == nil {
		t.Fatal("an archive that does not verify must be rejected")
	} else if e, ok := apperr.AsError(err); !ok || e.Code != "ARCHIVE_INVALID" {
		t.Fatalf("error = %v", err)
	}
	if _, err := store.Verify("replay_missing"); err == nil {
		t.Fatal("verifying an unknown archive must fail")
	}
}
