// Package invitation owns the asynchronous pre-match negotiation that S1 was
// missing: three generated proposals -> a structured EigenFlux PM -> one or
// more accept/reject/modify replies -> an all-confirmed invitation that can be
// turned into real seat claims for session.Manager.Create.
//
// The Hub never holds a model key. It generates proposals with the same frozen
// generator/ package used by the match referee, and it only talks to the
// outside world through services.EigenFluxPort (ADR-0001 decision 2/3).
//
// Wire shapes: docs/architecture/fluxspace-contracts.md §4.
package invitation

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand"
	"sync"
	"time"

	"fluxspace.ai/hub/apperr"
	"fluxspace.ai/hub/generator"
	"fluxspace.ai/hub/models"
	"fluxspace.ai/hub/services"
)

const (
	// ReplyKind and the three actions are declared in models/session.go.
	ProposalCount      = 3
	MaxModifyRounds    = 3
	DefaultTTLSeconds  = 300 // story 003 AC8: 5-minute invitation timeout
	MaxProposalRetries = 60  // aligns with generator.DedupMaxAttempts
)

// Status is the negotiation lifecycle.
type Status string

const (
	StatusDraft       Status = "draft"
	StatusProposed    Status = "proposed"
	StatusNegotiating Status = "negotiating"
	StatusConfirmed   Status = "confirmed"
	StatusRejected    Status = "rejected"
	StatusExpired     Status = "expired"
)

func fail(status int, code, format string, args ...any) error {
	return apperr.New(status, code, format, args...)
}

func isTerminal(s Status) bool {
	return s == StatusConfirmed || s == StatusRejected || s == StatusExpired
}

// Party is one side of a two-owner negotiation. OwnerID is the EigenFlux
// Agent owner, used later to enforce one companion per owner per match.
type Party struct {
	AgentID          string   `json:"agent_id"`
	CompanionID      string   `json:"companion_id"`
	OwnerID          string   `json:"owner_id"`
	DisplayName      string   `json:"display_name"`
	Confirmed        bool     `json:"confirmed"`
	SelectedProposal int      `json:"selected_proposal"`
	Amendments       []string `json:"amendments,omitempty"`
}

// Request is the body of POST /hub/invitations.
type Request struct {
	InvitationID   string                `json:"invitation_id"`
	Initiator      Party                 `json:"initiator"`
	Responder      Party                 `json:"responder"`
	Seats          int                   `json:"seats"`
	Pace           string                `json:"pace"`
	Mode           string                `json:"mode"`
	Seed           int64                 `json:"seed"`
	TTLSeconds     int                   `json:"ttl_seconds"`
	SocialContract models.SocialContract `json:"social_contract"`
}

// Negotiation is the observable state machine. Inbox is retained because the
// EigenFlux PM fetch marks messages as read upstream: persisting the returned
// slice immediately is a mandatory integration caution (contracts §7).
type Negotiation struct {
	ID             string                `json:"id"`
	Status         Status                `json:"status"`
	Invitation     models.Invitation     `json:"invitation"`
	Initiator      Party                 `json:"initiator"`
	Responder      Party                 `json:"responder"`
	ModifyRounds   int                   `json:"modify_rounds"`
	MessageIDs     []string              `json:"message_ids"`
	Inbox          []services.RawMessage `json:"inbox"`
	InvalidReplies []string              `json:"invalid_replies,omitempty"`
	CreatedAt      int64                 `json:"created_at"`
	UpdatedAt      int64                 `json:"updated_at"`
	Seats          int                   `json:"seats"`
	Pace           string                `json:"pace"`
	Mode           string                `json:"mode"`
	Seed           int64                 `json:"seed"`
	SessionID      string                `json:"session_id,omitempty"`

	applied   map[string]bool
	inboxSeen map[string]bool
	cursor    string
}

// Manager stores negotiations in memory. A production build persists them
// alongside replay archives in S4; keeping them in the Hub process is enough
// for S1's single-run proof.
type Manager struct {
	mu      sync.Mutex
	items   map[string]*Negotiation
	clock   func() int64
	counter int64
}

// NewManager builds a Manager with a wall-clock timestamp source.
func NewManager() *Manager {
	return NewManagerWithClock(func() int64 { return time.Now().UnixMilli() })
}

// NewManagerWithClock builds a Manager with an injectable clock (tests).
func NewManagerWithClock(clock func() int64) *Manager {
	if clock == nil {
		clock = func() int64 { return time.Now().UnixMilli() }
	}
	return &Manager{items: make(map[string]*Negotiation), clock: clock}
}

func clone(n *Negotiation) Negotiation {
	b, err := json.Marshal(n)
	if err != nil {
		return *n
	}
	var out Negotiation
	if err := json.Unmarshal(b, &out); err != nil {
		return *n
	}
	out.applied = make(map[string]bool, len(n.applied))
	for k, v := range n.applied {
		out.applied[k] = v
	}
	out.inboxSeen = make(map[string]bool, len(n.inboxSeen))
	for k, v := range n.inboxSeen {
		out.inboxSeen[k] = v
	}
	out.cursor = n.cursor
	return out
}

func (m *Manager) lookup(id string) (*Negotiation, error) {
	n, ok := m.items[id]
	if !ok {
		return nil, fail(404, "INVITATION_NOT_FOUND", "invitation %s not found", id)
	}
	return n, nil
}

// Get returns a detached copy of a negotiation.
func (m *Manager) Get(id string) (Negotiation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, err := m.lookup(id)
	if err != nil {
		return Negotiation{}, err
	}
	return clone(n), nil
}

// Create generates three structurally distinct proposals and records the
// initiator's confirmation of proposal 1. Sending the PM is a separate step so
// a transport failure cannot create a half-confirmed invitation.
func (m *Manager) Create(req Request) (Negotiation, error) {
	if req.Initiator.AgentID == "" || req.Responder.AgentID == "" {
		return Negotiation{}, fail(400, "PARTIES_REQUIRED", "initiator.agent_id and responder.agent_id are required")
	}
	if req.Initiator.OwnerID == "" || req.Responder.OwnerID == "" {
		return Negotiation{}, fail(400, "OWNERS_REQUIRED", "both parties need an owner_id")
	}
	if req.Initiator.OwnerID == req.Responder.OwnerID {
		return Negotiation{}, fail(409, "OWNER_SEAT_CONFLICT", "one owner cannot invite themselves into a match")
	}
	seats := req.Seats
	if seats == 0 {
		seats = 2
	}
	// The frozen prototype's generator caps the engine at MaxSelected (4)
	// AI participants. GDD's 2-8 range covers later game families; phase-1
	// deduction must not advertise seats it cannot generate.
	if seats < 2 || seats > generator.MaxSelected {
		return Negotiation{}, fail(400, "INVALID_SEAT_COUNT",
			"phase-1 deduction supports 2..%d seats, got %d", generator.MaxSelected, seats)
	}
	ttl := req.TTLSeconds
	if ttl <= 0 {
		ttl = DefaultTTLSeconds
	}
	contract := req.SocialContract
	if contract.Sharing == "" {
		contract = models.SocialContract{
			Sharing: "open", PrivateChannels: "allowed",
			ReplayConsent: "all", Visibility: "private",
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	m.counter++
	id := req.InvitationID
	if id == "" {
		id = fmt.Sprintf("inv_%d_%d", now, m.counter)
	}
	if _, exists := m.items[id]; exists {
		return Negotiation{}, fail(409, "INVITATION_ID_CONFLICT", "invitation %s already exists", id)
	}
	seed := req.Seed
	if seed == 0 {
		h := fnv.New64a()
		_, _ = h.Write([]byte(id))
		seed = int64(h.Sum64())
	}
	proposals, err := buildProposals(seed, seats, req.Pace, req.Mode, contract, 0)
	if err != nil {
		return Negotiation{}, fail(400, "PROPOSAL_GENERATION_FAILED", "%v", err)
	}
	initiator := req.Initiator
	initiator.Confirmed = true
	initiator.SelectedProposal = 1
	responder := req.Responder
	responder.Confirmed = false

	n := &Negotiation{
		ID: id, Status: StatusDraft,
		Invitation: models.Invitation{
			Kind:                models.InvitationKind,
			InvitationID:        id,
			FromAgentID:         initiator.AgentID,
			FromCompanionID:     initiator.CompanionID,
			ToAgentID:           responder.AgentID,
			Proposals:           proposals,
			SocialContractDraft: contract,
			EstimatedMinutes:    proposals[0].Minutes,
			ExpiresAt:           now + int64(ttl)*1000,
			SelectedProposal:    1,
		},
		Initiator: initiator, Responder: responder,
		CreatedAt: now, UpdatedAt: now, Seats: seats,
		Pace: req.Pace, Mode: req.Mode, Seed: seed,
		applied:   make(map[string]bool),
		inboxSeen: make(map[string]bool),
	}
	m.items[id] = n
	return clone(n), nil
}

// buildProposals produces three proposals with distinct fingerprints so the
// AI (or its local rule engine) genuinely has three different games to choose
// from. StructuralHash deliberately collides in the frozen prototype (it is
// topology-only), so GDD's "no repeated structural fingerprints within five
// matches" is enforced on generator.Fingerprint, not StructuralHash.
func buildProposals(seed int64, seats int, pace, mode string, contract models.SocialContract, rotation int) ([]models.Proposal, error) {
	out := make([]models.Proposal, 0, ProposalCount)
	seen := make(map[string]bool)
	base := seed + int64(rotation)*7919
	for attempt := 0; attempt < MaxProposalRetries && len(out) < ProposalCount; attempt++ {
		sk, err := generator.GenerateSkeleton(generator.GenerateOptions{
			Pace: pace, Mode: mode, CompanionCount: seats,
			HistoryFingerprints: keys(seen),
		}, rand.New(rand.NewSource(base+int64(attempt)*104729)))
		if err != nil {
			return nil, err
		}
		if seen[sk.Fingerprint] {
			continue
		}
		seen[sk.Fingerprint] = true
		summary := sk.Title
		if sk.ThemeText != "" {
			runes := []rune(sk.ThemeText)
			if len(runes) > 90 {
				runes = runes[:90]
			}
			summary += " - " + string(runes)
		}
		points := []string{
			fmt.Sprintf("%d seats, about %d minutes", seats, sk.EstimatedMinutes),
			fmt.Sprintf("sharing=%s private_channels=%s replay_consent=%s",
				contract.Sharing, contract.PrivateChannels, contract.ReplayConsent),
			fmt.Sprintf("%d rounds, fingerprint %s", len(sk.Rounds), sk.Fingerprint),
		}
		if len(sk.Rules) > 0 {
			points = append(points, sk.Rules[0])
		}
		out = append(out, models.Proposal{
			Index: len(out) + 1, Type: "deduction", Seats: seats,
			Minutes: sk.EstimatedMinutes, StructuralHash: sk.StructuralHash,
			ContentHash: sk.ContentHash, Summary: summary, ContractPoints: points,
		})
	}
	if len(out) < ProposalCount {
		return nil, fmt.Errorf("only %d distinct proposals after %d attempts", len(out), MaxProposalRetries)
	}
	return out, nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Send delivers the invitation as a structured PM. EnsureFriend comes first
// because PM requires an existing friend relation (contracts §7 caution 2).
// The PM body is hashed into request_id by the port, so a retried Send is
// idempotent against the gateway's 60-second dedup window.
func (m *Manager) Send(ctx context.Context, port services.EigenFluxPort, id string) (Negotiation, string, error) {
	if port == nil {
		return Negotiation{}, "", fail(503, "EIGENFLUX_PORT_UNCONFIGURED", "no EigenFluxPort configured on this Hub")
	}
	m.mu.Lock()
	n, err := m.lookup(id)
	if err != nil {
		m.mu.Unlock()
		return Negotiation{}, "", err
	}
	if err := m.checkActive(n); err != nil {
		m.mu.Unlock()
		return Negotiation{}, "", err
	}
	if n.Status == StatusProposed {
		m.mu.Unlock()
		return clone(n), n.MessageIDs[len(n.MessageIDs)-1], nil
	}
	body, err := json.Marshal(n.Invitation)
	if err != nil {
		m.mu.Unlock()
		return Negotiation{}, "", fail(500, "INVITATION_ENCODE_FAILED", "%v", err)
	}
	from, to := n.Initiator.AgentID, n.Responder.AgentID
	m.mu.Unlock()

	if err := port.EnsureFriend(ctx, from, to); err != nil {
		return Negotiation{}, "", fail(502, "FRIEND_REQUEST_FAILED", "%v", err)
	}
	msgID, err := port.SendPM(ctx, from, to, body)
	if err != nil {
		return Negotiation{}, "", fail(502, "PM_SEND_FAILED", "%v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	n, err = m.lookup(id)
	if err != nil {
		return Negotiation{}, "", err
	}
	if isTerminal(n.Status) {
		return Negotiation{}, "", fail(409, "INVITATION_TERMINAL", "invitation %s is %s", id, n.Status)
	}
	n.Status = StatusProposed
	n.MessageIDs = append(n.MessageIDs, msgID)
	n.UpdatedAt = m.clock()
	return clone(n), msgID, nil
}

func (m *Manager) checkActive(n *Negotiation) error {
	if isTerminal(n.Status) {
		return fail(409, "INVITATION_TERMINAL", "invitation %s is %s", n.ID, n.Status)
	}
	if m.clock() > n.Invitation.ExpiresAt {
		n.Status = StatusExpired
		n.UpdatedAt = m.clock()
		return fail(409, "INVITATION_EXPIRED", "invitation %s expired", n.ID)
	}
	return nil
}

// ApplyReply applies one accept/reject/modify. messageID is optional; when
// supplied it makes re-delivered PMs idempotent.
func (m *Manager) ApplyReply(id, messageID string, reply models.InvitationReply) (Negotiation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, err := m.lookup(id)
	if err != nil {
		return Negotiation{}, err
	}
	if messageID != "" && n.applied[messageID] {
		return clone(n), nil
	}
	if err := m.checkActive(n); err != nil {
		return Negotiation{}, err
	}
	if messageID != "" {
		n.applied[messageID] = true
	}
	if err := applyReplyLocked(n, reply, m.clock); err != nil {
		return Negotiation{}, err
	}
	return clone(n), nil
}

func applyReplyLocked(n *Negotiation, reply models.InvitationReply, clock func() int64) error {
	if reply.Kind != models.ReplyKind {
		return fail(400, "INVALID_REPLY_KIND", "kind must be %s, got %q", models.ReplyKind, reply.Kind)
	}
	if reply.InvitationID != n.ID {
		return fail(400, "INVITATION_ID_MISMATCH", "reply is for %s, negotiation is %s", reply.InvitationID, n.ID)
	}
	isInitiator := reply.AgentID == n.Initiator.AgentID
	isResponder := reply.AgentID == n.Responder.AgentID
	if !isInitiator && !isResponder {
		return fail(403, "UNKNOWN_REPLY_AGENT", "agent %s is not a party to %s", reply.AgentID, n.ID)
	}

	switch reply.Action {
	case models.ReplyReject:
		n.Status = StatusRejected
		if isResponder {
			n.Responder.Amendments = append(n.Responder.Amendments, reply.Amendments...)
		}
	case models.ReplyModify:
		if n.ModifyRounds >= MaxModifyRounds {
			return fail(409, "MODIFY_LIMIT_REACHED", "at most %d modify rounds are allowed", MaxModifyRounds)
		}
		n.ModifyRounds++
		party := &n.Responder
		if isInitiator {
			party = &n.Initiator
		}
		party.Amendments = append(party.Amendments, reply.Amendments...)
		proposals, err := buildProposals(n.Seed, n.Seats, n.Pace, n.Mode,
			n.Invitation.SocialContractDraft, n.ModifyRounds)
		if err != nil {
			return fail(500, "PROPOSAL_REGENERATION_FAILED", "%v", err)
		}
		n.Invitation.Proposals = proposals
		n.Invitation.SelectedProposal = 1
		n.Invitation.EstimatedMinutes = proposals[0].Minutes
		n.Initiator.Confirmed = true
		n.Initiator.SelectedProposal = 1
		n.Responder.Confirmed = false
		n.Responder.SelectedProposal = 0
		n.Status = StatusNegotiating
	case models.ReplyAccept:
		selected := reply.SelectedProposal
		if selected == 0 {
			selected = n.Invitation.SelectedProposal
		}
		if selected < 1 || selected > len(n.Invitation.Proposals) {
			return fail(400, "INVALID_SELECTED_PROPOSAL", "selected_proposal %d outside [1,%d]", selected, len(n.Invitation.Proposals))
		}
		if isInitiator {
			n.Initiator.Confirmed = true
			n.Initiator.SelectedProposal = selected
		} else {
			n.Responder.Confirmed = true
			n.Responder.SelectedProposal = selected
			if selected != n.Initiator.SelectedProposal {
				// A counter-proposal invalidates the initiator's earlier choice;
				// the initiator must explicitly confirm the new index.
				n.Initiator.Confirmed = false
				n.Status = StatusNegotiating
			}
		}
		if n.Initiator.Confirmed && n.Responder.Confirmed &&
			n.Initiator.SelectedProposal == n.Responder.SelectedProposal {
			n.Invitation.SelectedProposal = selected
			n.Status = StatusConfirmed
		} else if n.Status != StatusNegotiating {
			n.Status = StatusNegotiating
		}
	default:
		return fail(400, "INVALID_REPLY_ACTION", "action must be accept|reject|modify, got %q", reply.Action)
	}
	n.UpdatedAt = clock()
	return nil
}

// PollReplies is the receive half of a real PM round trip. It fetches the
// initiator's inbox, persists every returned message before interpreting it
// (fetch marks messages read upstream), ignores unrelated PMs and applies each
// valid reply at most once.
func (m *Manager) PollReplies(ctx context.Context, port services.EigenFluxPort, id string) (Negotiation, int, error) {
	if port == nil {
		return Negotiation{}, 0, fail(503, "EIGENFLUX_PORT_UNCONFIGURED", "no EigenFluxPort configured on this Hub")
	}
	m.mu.Lock()
	n, err := m.lookup(id)
	if err != nil {
		m.mu.Unlock()
		return Negotiation{}, 0, err
	}
	// Polling stays legal in a terminal state: the inbox still has to be
	// persisted, and re-delivered PMs must stay idempotent rather than raising
	// an error that looks like a transport failure.
	inboxAgent, cursor, responderID := n.Initiator.AgentID, n.cursor, n.Responder.AgentID
	m.mu.Unlock()

	messages, err := port.ListPM(ctx, inboxAgent, cursor)
	if err != nil {
		return Negotiation{}, 0, fail(502, "PM_FETCH_FAILED", "%v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	n, err = m.lookup(id)
	if err != nil {
		return Negotiation{}, 0, err
	}
	// Persist before interpreting anything (contracts §7 caution 3). A gateway
	// may redeliver the same message on a retried fetch, so the inbox is
	// deduplicated by msg_id instead of growing without bound.
	if n.inboxSeen == nil {
		n.inboxSeen = make(map[string]bool)
	}
	fresh := make([]services.RawMessage, 0, len(messages))
	for _, msg := range messages {
		key := msg.MsgID
		if key == "" {
			key = fmt.Sprintf("anon_%d_%d", msg.CreatedAt, len(n.Inbox)+len(fresh))
		}
		if n.inboxSeen[key] {
			continue
		}
		n.inboxSeen[key] = true
		n.Inbox = append(n.Inbox, msg)
		fresh = append(fresh, msg)
	}
	applied := 0
	for _, msg := range fresh {
		if msg.MsgID != "" {
			n.cursor = msg.MsgID
		}
		if n.applied[msg.MsgID] {
			continue
		}
		if msg.SenderID != responderID {
			continue
		}
		var reply models.InvitationReply
		if err := json.Unmarshal(msg.Content, &reply); err != nil {
			continue
		}
		if reply.Kind != models.ReplyKind || reply.InvitationID != n.ID {
			continue
		}
		if isTerminal(n.Status) {
			continue
		}
		n.applied[msg.MsgID] = true
		if err := applyReplyLocked(n, reply, m.clock); err != nil {
			n.InvalidReplies = append(n.InvalidReplies, fmt.Sprintf("%s: %v", msg.MsgID, err))
			continue
		}
		applied++
	}
	n.UpdatedAt = m.clock()
	return clone(n), applied, nil
}

// BuildClaims converts a confirmed negotiation into the exact inputs of
// session.Manager.Create. Missing seats are filled with clearly labelled
// official companions (GDD "one companion per owner per match").
func (m *Manager) BuildClaims(id string, backfill []models.SeatClaim) (models.Invitation, []models.SeatClaim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, err := m.lookup(id)
	if err != nil {
		return models.Invitation{}, nil, err
	}
	if n.Status != StatusConfirmed {
		return models.Invitation{}, nil, fail(409, "INVITATION_NOT_CONFIRMED",
			"invitation %s is %s, all parties must confirm first", id, n.Status)
	}
	if m.clock() > n.Invitation.ExpiresAt {
		n.Status = StatusExpired
		return models.Invitation{}, nil, fail(409, "INVITATION_EXPIRED", "invitation %s expired", id)
	}
	claims := []models.SeatClaim{
		seat(0, n.Initiator),
		seat(1, n.Responder),
	}
	for i := 2; i < n.Seats; i++ {
		if i-2 < len(backfill) {
			c := backfill[i-2]
			c.SeatID = fmt.Sprintf("seat_%d", i)
			c.Official = true
			claims = append(claims, c)
			continue
		}
		claims = append(claims, models.SeatClaim{
			SeatID:      fmt.Sprintf("seat_%d", i),
			OwnerID:     fmt.Sprintf("official:%s:%d", id, i),
			AgentID:     fmt.Sprintf("official_agent_%d", i),
			CompanionID: fmt.Sprintf("official_companion_%d", i),
			DisplayName: fmt.Sprintf("Official Companion %d (backfill)", i-1),
			Official:    true,
		})
	}
	return n.Invitation, claims, nil
}

func seat(i int, p Party) models.SeatClaim {
	name := p.DisplayName
	if name == "" {
		name = p.CompanionID
	}
	return models.SeatClaim{
		SeatID:      fmt.Sprintf("seat_%d", i),
		OwnerID:     p.OwnerID,
		AgentID:     p.AgentID,
		CompanionID: p.CompanionID,
		DisplayName: name,
	}
}

// MarkSessionCreated records the session ID so a confirmed negotiation cannot
// silently spawn a second match.
func (m *Manager) MarkSessionCreated(id, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, err := m.lookup(id)
	if err != nil {
		return err
	}
	if n.SessionID != "" {
		return fail(409, "SESSION_ALREADY_CREATED", "invitation %s already created %s", id, n.SessionID)
	}
	n.SessionID = sessionID
	n.UpdatedAt = m.clock()
	return nil
}
