// Package session owns the Hub's deterministic match state machine: seat
// binding, the four-phase round loop, decision validation, settlement and the
// per-session event log used by SSE and replay archives.
//
// Design rules, in priority order:
//   - docs/architecture/fluxspace-contracts.md §3/§4/§5 defines the wire shape.
//   - design/game-brief.md defines the phases, bounds and endings.
//   - referee/ and generator/ own the maths. This package never re-implements it.
//
// The Hub does not proxy model calls (ADR-0001 decision 3). Clients submit an
// already-validated decision; the Hub validates it again against the live
// action pool before accepting it.
package session

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand"
	"sync"
	"time"

	"fluxspace.ai/hub/apperr"
	"fluxspace.ai/hub/generator"
	"fluxspace.ai/hub/models"
	"fluxspace.ai/hub/referee"
)

// Error is an alias retained for callers that already refer to session.Error.
// The definition now lives in apperr so business packages do not depend on the
// session package merely to build an error.
type Error = apperr.Error

func fail(status int, code, format string, args ...any) error {
	return apperr.New(status, code, format, args...)
}

// AsError extracts a domain *Error from err, if present.
func AsError(err error) (*Error, bool) { return apperr.AsError(err) }

// Clock is injectable so tests can pin event timestamps.
type Clock func() int64

// CreateRequest is the body of POST /hub/sessions. The skeleton is generated
// by the Hub from Seed, never supplied by the caller: an externally supplied
// skeleton could break determinism and the calibration invariants.
type CreateRequest struct {
	Invitation     models.Invitation  `json:"invitation"`
	Claims         []models.SeatClaim `json:"claims"`
	Seed           int64              `json:"seed"`
	CompanionCount int                `json:"companion_count"`
	Pace           string             `json:"pace"`
	Mode           string             `json:"mode"`
}

// Manager holds all sessions. mu protects the map and every session's state;
// SSE delivery is a stateless poll over the retained event log, so a slow
// observer can never block settlement or lose an event.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*state
	owners   map[string]string // owner ID -> live session ID
	clock    Clock
	counter  int64
}

type state struct {
	view       models.SessionView
	invitation models.Invitation
	events     []models.Event
	rng        *rand.Rand
	usage      map[string]int
	entries    []referee.RoundEntry
	talkCounts map[string]int
	decisions  map[string]models.Decision
}

// NewManager builds a Manager with a wall-clock timestamp source.
func NewManager() *Manager {
	return NewManagerWithClock(func() int64 { return time.Now().UnixMilli() })
}

// NewManagerWithClock builds a Manager with an injectable clock (tests).
func NewManagerWithClock(clock Clock) *Manager {
	if clock == nil {
		clock = func() int64 { return time.Now().UnixMilli() }
	}
	return &Manager{
		sessions: make(map[string]*state),
		owners:   make(map[string]string),
		clock:    clock,
	}
}

func seedFrom(invitationID string, seed int64) int64 {
	if seed != 0 {
		return seed
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(invitationID))
	return int64(h.Sum64())
}

func (m *Manager) nextSessionID() string {
	m.counter++
	return fmt.Sprintf("sess_%d_%d", m.clock(), m.counter)
}

func (m *Manager) nextEventID(s *state) int64 {
	if len(s.events) == 0 {
		return 1
	}
	return s.events[len(s.events)-1].ID + 1
}

// Create validates the confirmed invitation, enforces the two S1 invariants
// (seat uniqueness, one live match per owner), generates the skeleton and
// opens round 1 in the observe phase.
func (m *Manager) Create(req CreateRequest) (models.SessionView, error) {
	inv := req.Invitation
	if inv.Kind != "fluxspace.invitation.v1" {
		return models.SessionView{}, fail(400, "INVALID_INVITATION_KIND", "kind must be fluxspace.invitation.v1, got %q", inv.Kind)
	}
	if inv.InvitationID == "" {
		return models.SessionView{}, fail(400, "INVITATION_ID_REQUIRED", "invitation_id is required")
	}
	if len(inv.Proposals) == 0 {
		return models.SessionView{}, fail(400, "PROPOSALS_REQUIRED", "at least one proposal is required")
	}
	if inv.SelectedProposal < 1 || inv.SelectedProposal > len(inv.Proposals) {
		return models.SessionView{}, fail(400, "INVALID_SELECTED_PROPOSAL", "selected_proposal %d outside [1,%d]", inv.SelectedProposal, len(inv.Proposals))
	}
	if len(req.Claims) < 2 || len(req.Claims) > 8 {
		return models.SessionView{}, fail(400, "INVALID_SEAT_COUNT", "2..8 seats required, got %d", len(req.Claims))
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	seats := make([]models.Seat, 0, len(req.Claims))
	seenSeats := make(map[string]bool, len(req.Claims))
	seenOwners := make(map[string]string, len(req.Claims))
	official := make([]string, 0, len(req.Claims))
	for _, c := range req.Claims {
		if c.SeatID == "" || c.OwnerID == "" || c.CompanionID == "" {
			return models.SessionView{}, fail(400, "INCOMPLETE_SEAT", "seat_id, owner_id and companion_id are required")
		}
		if seenSeats[c.SeatID] {
			return models.SessionView{}, fail(409, "DUPLICATE_SEAT", "seat %q claimed twice", c.SeatID)
		}
		seenSeats[c.SeatID] = true
		if other, dup := seenOwners[c.OwnerID]; dup {
			return models.SessionView{}, fail(409, "OWNER_SEAT_CONFLICT",
				"owner %q claims both %q and %q; one companion per owner per match", c.OwnerID, other, c.SeatID)
		}
		seenOwners[c.OwnerID] = c.SeatID
		if live, busy := m.owners[c.OwnerID]; busy {
			return models.SessionView{}, fail(409, "OWNER_HAS_LIVE_MATCH",
				"owner %q already has live session %s", c.OwnerID, live)
		}
		seats = append(seats, models.Seat{SeatClaim: c})
		if c.Official {
			official = append(official, c.SeatID)
		}
	}

	seed := seedFrom(inv.InvitationID, req.Seed)
	count := req.CompanionCount
	if count == 0 {
		count = len(req.Claims)
	}
	// generator.MaxSelected is 4 in the frozen prototype. Claims may still be
	// 2..8 per contracts §3, but a deduction skeleton cannot be generated for
	// more than 4 AI participants; reject that explicitly instead of letting
	// GenerateSkeleton fail with an opaque 400 mid-creation.
	if count > generator.MaxSelected {
		return models.SessionView{}, fail(400, "UNSUPPORTED_SEAT_COUNT",
			"phase-1 deduction supports at most %d AI participants, got %d", generator.MaxSelected, count)
	}
	skeleton, err := generator.GenerateSkeleton(generator.GenerateOptions{
		Mode:           req.Mode,
		Pace:           req.Pace,
		CompanionCount: count,
	}, rand.New(rand.NewSource(seed)))
	if err != nil {
		return models.SessionView{}, fail(400, "SKELETON_GENERATION_FAILED", "%v", err)
	}

	id := m.nextSessionID()
	metrics := make(map[string]models.Metric, len(skeleton.InitialMetrics))
	for _, metric := range skeleton.InitialMetrics {
		metrics[metric.Key] = metric
	}
	s := &state{
		view: models.SessionView{
			SessionID:       id,
			InvitationID:    inv.InvitationID,
			Status:          models.SessionLobby,
			Round:           1,
			TotalRounds:     len(skeleton.Rounds),
			Phase:           models.PhaseObserve,
			Seats:           seats,
			Metrics:         metrics,
			Skeleton:        skeleton,
			Decisions:       make(map[string]models.Decision),
			TalkCounts:      make(map[string]int),
			SocialContract:  inv.SocialContractDraft,
			OfficialSeatIDs: official,
		},
		invitation: inv,
		rng:        rand.New(rand.NewSource(seed)),
		usage:      make(map[string]int),
		talkCounts: make(map[string]int),
		decisions:  make(map[string]models.Decision),
	}
	m.sessions[id] = s
	for owner := range seenOwners {
		m.owners[owner] = id
	}

	m.publish(s, models.EventMatchStart, models.VisibilityPublic, "", map[string]any{
		"invitation_id":     inv.InvitationID,
		"selected_proposal": inv.SelectedProposal,
		"structural_hash":   skeleton.StructuralHash,
		"content_hash":      skeleton.ContentHash,
		"official_seats":    official,
		"seats":             seats,
	})
	m.announceRound(s)
	return copyView(s.view), nil
}

// Join binds a seat to its owner. A session goes live when every seat joined.
func (m *Manager) Join(sessionID, seatID, ownerID string) (models.SessionView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.lookup(sessionID)
	if err != nil {
		return models.SessionView{}, err
	}
	if s.view.Status == models.SessionFinished {
		return models.SessionView{}, fail(409, "SESSION_FINISHED", "session %s is finished", sessionID)
	}
	idx := seatIndex(s, seatID)
	if idx < 0 {
		return models.SessionView{}, fail(404, "SEAT_NOT_FOUND", "seat %q is not in session %s", seatID, sessionID)
	}
	seat := &s.view.Seats[idx]
	if seat.OwnerID != ownerID {
		return models.SessionView{}, fail(403, "OWNER_MISMATCH", "seat %q belongs to owner %q, not %q", seatID, seat.OwnerID, ownerID)
	}
	if live, busy := m.owners[ownerID]; busy && live != sessionID {
		return models.SessionView{}, fail(409, "OWNER_HAS_LIVE_MATCH", "owner %q already has live session %s", ownerID, live)
	}
	if seat.Joined {
		return models.SessionView{}, fail(409, "SEAT_ALREADY_JOINED", "seat %q already joined", seatID)
	}
	seat.Joined = true
	m.owners[ownerID] = sessionID

	joined := 0
	for _, candidate := range s.view.Seats {
		if candidate.Joined {
			joined++
		}
	}
	if joined == len(s.view.Seats) && s.view.Status == models.SessionLobby {
		s.view.Status = models.SessionLive
	}
	m.publish(s, models.EventPhaseChanged, models.VisibilityPublic, seatID, map[string]any{
		"action": "join", "seat_id": seatID, "official": seat.Official,
		"joined": joined, "total": len(s.view.Seats), "status": s.view.Status,
	})
	return copyView(s.view), nil
}

// Advance moves the phase forward along observe -> discuss -> act. Resolve is
// entered implicitly when every live seat has submitted a decision; the Hub
// never sleeps for the GDD's wall-clock phase durations, the client owns them.
func (m *Manager) Advance(sessionID string, target models.Phase) (models.SessionView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.lookup(sessionID)
	if err != nil {
		return models.SessionView{}, err
	}
	if s.view.Status != models.SessionLive {
		return models.SessionView{}, fail(409, "SESSION_NOT_LIVE", "session %s is %s, not live", sessionID, s.view.Status)
	}
	want := nextPhase(s.view.Phase)
	if want == "" {
		return models.SessionView{}, fail(409, "AWAITING_DECISIONS", "phase %s resolves when all seats submit decisions", s.view.Phase)
	}
	if target != want {
		return models.SessionView{}, fail(409, "INVALID_PHASE_TRANSITION", "cannot advance %s -> %s, expected %s", s.view.Phase, target, want)
	}
	s.view.Phase = target
	if target == models.PhaseAct {
		s.entries = s.entries[:0]
		s.decisions = make(map[string]models.Decision)
	}
	m.publish(s, models.EventPhaseChanged, models.VisibilityPublic, "", map[string]any{
		"phase": target, "round": s.view.Round,
	})
	return copyView(s.view), nil
}

func nextPhase(current models.Phase) models.Phase {
	switch current {
	case models.PhaseObserve:
		return models.PhaseDiscuss
	case models.PhaseDiscuss:
		return models.PhaseAct
	default:
		return ""
	}
}

// PostDiscussion records one bounded discussion line. Private channels are
// delivered only to the sending seat and to the omniscient spectator stream.
func (m *Manager) PostDiscussion(sessionID string, msg models.DiscussionMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.lookup(sessionID)
	if err != nil {
		return err
	}
	if s.view.Status != models.SessionLive || s.view.Phase != models.PhaseDiscuss {
		return fail(409, "NOT_IN_DISCUSS", "session %s is %s/%s", sessionID, s.view.Status, s.view.Phase)
	}
	if seatIndex(s, msg.SeatID) < 0 || !s.view.Seats[seatIndex(s, msg.SeatID)].Joined {
		return fail(403, "SEAT_NOT_ACTIVE", "seat %q is not an active participant", msg.SeatID)
	}
	runes := []rune(msg.Text)
	if len(runes) == 0 || len(runes) > 200 {
		return fail(400, "INVALID_DISCUSSION_LENGTH", "discussion text must be 1..200 characters, got %d", len(runes))
	}
	if s.talkCounts[msg.SeatID] >= 2 {
		return fail(409, "DISCUSSION_QUOTA_EXCEEDED", "seat %q already used its 2 lines this round", msg.SeatID)
	}
	channel := msg.Channel
	visibility := models.VisibilityPublic
	if channel == "private" {
		visibility = models.VisibilityPrivate
	} else if channel != "public" {
		return fail(400, "INVALID_CHANNEL", "channel must be public or private, got %q", channel)
	}
	s.talkCounts[msg.SeatID]++
	s.view.TalkCounts[msg.SeatID] = s.talkCounts[msg.SeatID]
	m.publish(s, models.EventDiscussion, visibility, msg.SeatID, map[string]any{
		"seat_id": msg.SeatID, "text": msg.Text, "channel": channel,
		"round": s.view.Round, "line": s.talkCounts[msg.SeatID],
	})
	return nil
}

// SubmitAction validates a client-side decision against the live action pool
// and settles the round as soon as every active seat has submitted.
func (m *Manager) SubmitAction(sessionID, seatID string, sub models.DecisionSubmission) (models.SessionView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.lookup(sessionID)
	if err != nil {
		return models.SessionView{}, err
	}
	if s.view.Status != models.SessionLive || s.view.Phase != models.PhaseAct {
		return models.SessionView{}, fail(409, "NOT_IN_ACT", "session %s is %s/%s, decisions are only accepted in live/act", sessionID, s.view.Status, s.view.Phase)
	}
	idx := seatIndex(s, seatID)
	if idx < 0 || !s.view.Seats[idx].Joined {
		return models.SessionView{}, fail(403, "SEAT_NOT_ACTIVE", "seat %q is not an active participant", seatID)
	}
	if _, dup := s.decisions[seatID]; dup {
		return models.SessionView{}, fail(409, "DECISION_ALREADY_SUBMITTED", "seat %q already decided this round", seatID)
	}
	if len([]rune(sub.ReasoningSummary)) > 200 {
		return models.SessionView{}, fail(400, "REASONING_TOO_LONG", "reasoning_summary exceeds 200 characters")
	}
	if sub.Confidence < 0 || sub.Confidence > 100 {
		return models.SessionView{}, fail(400, "INVALID_CONFIDENCE", "confidence must be within [0,100], got %v", sub.Confidence)
	}
	action, ok := actionForCurrentRound(s, sub.ActionID)
	if !ok {
		return models.SessionView{}, fail(400, "ILLEGAL_ACTION_ID", "action %q is not in round %d's action pool", sub.ActionID, s.view.Round)
	}

	decision := models.Decision{
		ActionID:         sub.ActionID,
		ReasoningSummary: sub.ReasoningSummary,
		Confidence:       sub.Confidence,
		Source:           sub.Source,
	}
	if decision.Source == "" {
		decision.Source = "client"
	}
	s.decisions[seatID] = decision
	s.view.Decisions[seatID] = decision
	s.entries = append(s.entries, referee.RoundEntry{
		ActorID: seatID, IsPlayer: false, Side: "team", Action: action,
	})

	summaries := models.DensitySummaries{
		OneLiner:   fmt.Sprintf("%s chose %s", s.view.Seats[idx].DisplayName, action.Label),
		Structured: fmt.Sprintf("action_id=%s family=%s risk=%s source=%s confidence=%.0f", action.ID, action.Family, action.Risk, decision.Source, decision.Confidence),
		Comparison: decision.ReasoningSummary,
	}
	m.publish(s, models.EventDecision, models.VisibilityPublic, seatID, map[string]any{
		"seat_id":   seatID,
		"action_id": action.ID,
		"summaries": summaries,
		"official":  s.view.Seats[idx].Official,
	})

	if len(s.decisions) == activeSeatCount(s) {
		m.settle(s)
	}
	return copyView(s.view), nil
}

func (m *Manager) settle(s *state) {
	s.view.Phase = models.PhaseResolve
	delta := referee.ComputeRoundDelta(s.entries, s.view.Metrics, "team", "coop", s.usage, s.view.Round)
	for key, change := range delta.Delta {
		metric, ok := s.view.Metrics[key]
		if !ok {
			continue
		}
		metric.Value = clampMetric(metric.Value+change, metric.Max)
		s.view.Metrics[key] = metric
	}
	for key, change := range s.view.Skeleton.RoundTick {
		metric, ok := s.view.Metrics[key]
		if !ok {
			continue
		}
		metric.Value = clampMetric(metric.Value+change, metric.Max)
		s.view.Metrics[key] = metric
	}
	for _, entry := range s.entries {
		s.usage[entry.Action.Family]++
	}

	m.publish(s, models.EventResolution, models.VisibilityPublic, "", map[string]any{
		"round": s.view.Round, "delta": delta.Delta, "agreement": delta.Agreement,
		"synergy": delta.Synergy, "metrics": s.view.Metrics, "contribution": delta.Contribution,
	})

	if branch := pickBranch(s); branch != nil {
		for key, change := range branch.Effects {
			metric, ok := s.view.Metrics[key]
			if !ok {
				continue
			}
			metric.Value = clampMetric(metric.Value+change, metric.Max)
			s.view.Metrics[key] = metric
		}
		m.publish(s, models.EventBranchEvent, models.VisibilityPublic, "", map[string]any{
			"branch_id": branch.ID, "text": branch.Text,
			"effects": branch.Effects, "metrics": s.view.Metrics,
		})
	}

	outcome := referee.CheckEnd(s.view.Skeleton.WinCondition, s.view.Skeleton.LoseCondition,
		s.view.Metrics, s.view.Round, s.view.TotalRounds)
	if outcome != "" {
		s.view.Status = models.SessionFinished
		s.view.Outcome = string(outcome)
		s.view.Phase = models.PhaseResolve
		m.publish(s, models.EventMatchEnd, models.VisibilityPublic, "", map[string]any{
			"outcome": string(outcome), "round": s.view.Round,
			"metrics": s.view.Metrics, "total_rounds": s.view.TotalRounds,
		})
		for _, seat := range s.view.Seats {
			if m.owners[seat.OwnerID] == s.view.SessionID {
				delete(m.owners, seat.OwnerID)
			}
		}
		return
	}

	s.view.Round++
	s.view.Phase = models.PhaseObserve
	s.entries = s.entries[:0]
	s.decisions = make(map[string]models.Decision)
	s.view.Decisions = make(map[string]models.Decision)
	s.talkCounts = make(map[string]int)
	s.view.TalkCounts = make(map[string]int)
	m.announceRound(s)
}

// pickBranch deterministically selects at most one resolve-phase branch whose
// condition holds, weighted by Branch.Weight. The frozen prototype applies
// branch events during resolve; using the Manager's seeded RNG keeps a match
// reproducible from its invitation ID and seed.
func pickBranch(s *state) *models.Branch {
	eligible := make([]*models.Branch, 0, len(s.view.Skeleton.Branches))
	total := 0
	for i := range s.view.Skeleton.Branches {
		b := &s.view.Skeleton.Branches[i]
		if b.When != "resolve" {
			continue
		}
		if b.Condition != nil && !referee.EvalCondition(*b.Condition, s.view.Metrics) {
			continue
		}
		if b.Weight <= 0 {
			continue
		}
		eligible = append(eligible, b)
		total += b.Weight
	}
	if total == 0 {
		return nil
	}
	roll := s.rng.Intn(total)
	for _, b := range eligible {
		if roll < b.Weight {
			return b
		}
		roll -= b.Weight
	}
	return eligible[len(eligible)-1]
}

func (m *Manager) announceRound(s *state) {
	round := s.view.Skeleton.Rounds[s.view.Round-1]
	m.publish(s, models.EventRoundBegin, models.VisibilityPublic, "", map[string]any{
		"round": s.view.Round, "title": round.Title,
		"objective": round.Objective, "actions": round.Actions,
	})
	m.publish(s, models.EventInformationReveal, models.VisibilityPublic, "", map[string]any{
		"round": s.view.Round, "reveal": round.Reveal, "tension": round.Tension,
	})
	// Omniscient spectators see every secret, including the ones withheld from
	// participant streams (GDD: "all game secrets visible to spectators").
	m.publish(s, models.EventInformationReveal, models.VisibilityOmniscient, "", map[string]any{
		"round": s.view.Round, "reveal": round.Reveal,
		"all_round_reveals": allReveals(s.view.Skeleton),
		"win_condition":     s.view.Skeleton.WinCondition,
		"lose_condition":    s.view.Skeleton.LoseCondition,
	})
}

func allReveals(sk *models.Skeleton) []models.Clue {
	out := make([]models.Clue, 0, len(sk.Rounds))
	for _, r := range sk.Rounds {
		out = append(out, r.Reveal...)
	}
	return out
}

func (m *Manager) publish(s *state, eventType, visibility, seatID string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		body = []byte(`{}`)
	}
	snapshot, err := json.Marshal(map[string]any{
		"round": s.view.Round, "phase": s.view.Phase, "status": s.view.Status,
		"metrics": s.view.Metrics, "outcome": s.view.Outcome,
	})
	if err != nil {
		snapshot = []byte(`{}`)
	}
	ev := models.Event{
		ID: m.nextEventID(s), Type: eventType, SessionID: s.view.SessionID,
		Round: s.view.Round, Phase: s.view.Phase, SeatID: seatID,
		Visibility: visibility, Payload: body, StateSnapshot: snapshot, TS: m.clock(),
	}
	s.events = append(s.events, ev)
	s.view.LastEventID = ev.ID
}

// EventsSince returns every retained event after lastEventID that the caller's
// role may see. SSE handlers poll this; snapshot recovery uses the same log.
func (m *Manager) EventsSince(sessionID, seatID string, omniscient bool, lastEventID int64) ([]models.Event, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.lookup(sessionID)
	if err != nil {
		return nil, 0, err
	}
	latest := int64(0)
	if len(s.events) > 0 {
		latest = s.events[len(s.events)-1].ID
	}
	out := make([]models.Event, 0, len(s.events))
	for _, ev := range s.events {
		if ev.ID <= lastEventID || !visibleTo(ev, seatID, omniscient) {
			continue
		}
		out = append(out, ev)
	}
	return out, latest, nil
}

func visibleTo(ev models.Event, seatID string, omniscient bool) bool {
	if omniscient {
		return true
	}
	switch ev.Visibility {
	case models.VisibilityPublic:
		return true
	case models.VisibilityPrivate:
		return ev.SeatID != "" && ev.SeatID == seatID
	default:
		return false
	}
}

// State returns a detached copy of the session view for GET /hub/sessions/{id}.
func (m *Manager) State(sessionID string) (models.SessionView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.lookup(sessionID)
	if err != nil {
		return models.SessionView{}, err
	}
	return copyView(s.view), nil
}

// Snapshot is the reconnect recovery payload (contracts §3). It is a full,
// detached state copy; SSE clients replay from Last-Event-ID on top of it.
func (m *Manager) Snapshot(sessionID string) (models.SessionView, error) {
	return m.State(sessionID)
}

func (m *Manager) lookup(sessionID string) (*state, error) {
	s, ok := m.sessions[sessionID]
	if !ok {
		return nil, fail(404, "SESSION_NOT_FOUND", "session %s not found", sessionID)
	}
	return s, nil
}

func seatIndex(s *state, seatID string) int {
	for i := range s.view.Seats {
		if s.view.Seats[i].SeatID == seatID {
			return i
		}
	}
	return -1
}

func activeSeatCount(s *state) int {
	n := 0
	for _, seat := range s.view.Seats {
		if seat.Joined {
			n++
		}
	}
	return n
}

func actionForCurrentRound(s *state, actionID string) (models.Action, bool) {
	round := s.view.Skeleton.Rounds[s.view.Round-1]
	for _, action := range round.Actions {
		if action.ID == actionID {
			return action, true
		}
	}
	return models.Action{}, false
}

func clampMetric(value, max float64) float64 {
	if value < 0 {
		return 0
	}
	if max > 0 && value > max {
		return max
	}
	return value
}

// copyView deep-copies through JSON so callers can never mutate Manager state
// through a returned view (maps, slices and the skeleton are all shared).
func copyView(v models.SessionView) models.SessionView {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out models.SessionView
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	return out
}
