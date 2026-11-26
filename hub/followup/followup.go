// Package followup implements the post-match interaction named by the S1 goal
// and left out of the first Hub pass: a source-tagged recap, draft directional
// relationship updates and unresolved threads, delivered to each real owner
// over an EigenFlux PM.
//
// S1 scope is deliberately narrow. Memories and relationships are NOT written
// to a local database here (that is S4), and no model is called. Every sentence
// in a follow-up is derived from a retained Hub event, so the document cannot
// invent reasoning a companion never produced.
package followup

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"fluxspace.ai/hub/apperr"
	"fluxspace.ai/hub/models"
	"fluxspace.ai/hub/services"
)

const (
	MaxMemoriesPerCompanion = 3 // GDD reflection bound: 1-3 entries
	MaxRelationshipDrafts   = 6
	MaxUnresolvedThreads    = 3
)

func fail(status int, code, format string, args ...any) error {
	return apperr.New(status, code, format, args...)
}

// RelationshipUpdate is a DRAFT suggestion, never an applied relationship
// change. The owner confirms it in the client before anything is persisted
// (S4 owns the relationships table).
type RelationshipUpdate struct {
	FromCompanionID string             `json:"from_companion_id"`
	ToCompanionID   string             `json:"to_companion_id"`
	Direction       string             `json:"direction"` // toward | away
	Reason          string             `json:"reason"`
	SourceEventID   int64              `json:"source_event_id"`
	SuggestedDelta  map[string]float64 `json:"suggested_delta"`
	Draft           bool               `json:"draft"`
}

// Document is the structured JSON carried in a follow-up PM content field.
type Document struct {
	Kind                 string               `json:"kind"`
	SessionID            string               `json:"session_id"`
	InvitationID         string               `json:"invitation_id"`
	Outcome              string               `json:"outcome"`
	Rounds               int                  `json:"rounds"`
	TotalRounds          int                  `json:"total_rounds"`
	RecipientCompanionID string               `json:"recipient_companion_id,omitempty"`
	Memories             []models.Memory      `json:"memories"`
	RelationshipUpdates  []RelationshipUpdate `json:"relationship_updates"`
	UnresolvedThreads    []string             `json:"unresolved_threads"`
	GeneratedAt          int64                `json:"generated_at"`
}

// Delivery records one successful PM send.
type Delivery struct {
	AgentID      string `json:"agent_id"`
	CompanionID  string `json:"companion_id"`
	MessageID    string `json:"message_id"`
	MemoryCount  int    `json:"memory_count"`
	Relationship int    `json:"relationship_draft_count"`
	// Card reports the post-match Agent Card projection for this recipient.
	// Nil means projection was not configured; a non-nil record always says
	// what was written or why nothing was.
	Card *CardProjectionRecord `json:"card_projection,omitempty"`
}

// CardProjectionRecord is the delivery-side view of one Agent Card projection.
type CardProjectionRecord struct {
	Written []string                       `json:"written,omitempty"`
	Skipped []services.CardProjectionField `json:"skipped,omitempty"`
	Error   string                         `json:"error,omitempty"`
}

// Sender delivers follow-ups through the real EigenFluxPort. HubAgentID is the
// Agent identity the Hub sends from; it must be configured explicitly so the
// product never pretends an unauthenticated send succeeded.
//
// CardFields is the opt-in list of Agent Card fields the Hub may fill with a
// post-match summary. It is empty by default: the Hub does not touch an owner's
// public card unless an operator asked for it, and even then only blank fields
// are filled (services.GatewayPort.ProjectAgentCardFields enforces that).
type Sender struct {
	Port       services.EigenFluxPort
	HubAgentID string
	CardFields []string

	mu      sync.Mutex
	history map[string][]Delivery
}

// Deliveries returns a detached copy of the delivery record for a session.
func (s *Sender) Deliveries(sessionID string) []Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Delivery(nil), s.history[sessionID]...)
}

// Build derives a follow-up from a finished session's retained event log.
// Private discussion events are excluded from the shared document: a
// follow-up PM must not leak another seat's private channel.
func Build(view models.SessionView, events []models.Event, now int64) (Document, error) {
	if view.Status != models.SessionFinished {
		return Document{}, fail(409, "SESSION_NOT_FINISHED",
			"session %s is %s, a follow-up requires a finished match", view.SessionID, view.Status)
	}
	if view.Outcome == "" {
		return Document{}, fail(409, "OUTCOME_MISSING", "session %s has no recorded outcome", view.SessionID)
	}
	doc := Document{
		Kind: models.FollowUpKind, SessionID: view.SessionID,
		InvitationID: view.InvitationID, Outcome: view.Outcome,
		Rounds: view.Round, TotalRounds: view.TotalRounds, GeneratedAt: now,
		Memories: make([]models.Memory, 0), RelationshipUpdates: make([]RelationshipUpdate, 0),
		UnresolvedThreads: make([]string, 0),
	}

	bySeat := make(map[string]models.Seat, len(view.Seats))
	for _, seat := range view.Seats {
		bySeat[seat.SeatID] = seat
	}
	perCompanion := make(map[string]int)
	for _, ev := range events {
		if ev.Visibility == models.VisibilityPrivate {
			continue
		}
		if ev.Type == models.EventInformationReveal || ev.Type == models.EventPhaseChanged {
			continue // logistics, not a social fact
		}
		companionID := ""
		if seat, ok := bySeat[ev.SeatID]; ok {
			companionID = seat.CompanionID
		}
		if ev.Type == models.EventMatchEnd || ev.Type == models.EventResolution ||
			ev.Type == models.EventBranchEvent {
			// Shared facts belong to every real companion, capped per companion.
			for _, seat := range view.Seats {
				if seat.Official || seat.CompanionID == "" {
					continue
				}
				doc.appendMemory(view, ev, seat, &perCompanion)
			}
			continue
		}
		if companionID == "" {
			continue
		}
		doc.appendMemory(view, ev, bySeat[ev.SeatID], &perCompanion)
	}

	doc.RelationshipUpdates = draftRelationships(view, events)
	doc.UnresolvedThreads = unresolvedThreads(view)
	return doc, nil
}

func (d *Document) appendMemory(view models.SessionView, ev models.Event, seat models.Seat, count *map[string]int) {
	if (*count)[seat.CompanionID] >= MaxMemoriesPerCompanion {
		return
	}
	content := describe(ev)
	if content == "" {
		return
	}
	(*count)[seat.CompanionID]++
	d.Memories = append(d.Memories, models.Memory{
		ID:          fmt.Sprintf("mem_%s_%d_%s", view.SessionID, ev.ID, seat.SeatID),
		CompanionID: seat.CompanionID,
		Kind:        memoryKind(ev.Type),
		Content:     content,
		Source: models.MemorySource{
			Kind:  ev.Type,
			RefID: fmt.Sprintf("%s:%d", view.SessionID, ev.ID),
			Round: ev.Round,
		},
		Strength:  strength(ev.Type),
		CreatedAt: ev.TS,
	})
}

// describe only quotes fields that actually exist in the event payload. No
// model reasoning, sentiment or motive is inferred.
func describe(ev models.Event) string {
	var p map[string]any
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return ""
	}
	str := func(key string) string {
		if v, ok := p[key].(string); ok {
			return v
		}
		return ""
	}
	switch ev.Type {
	case models.EventDecision:
		oneLiner := ""
		if s, ok := p["summaries"].(map[string]any); ok {
			oneLiner, _ = s["one_liner"].(string)
		}
		if oneLiner != "" {
			return fmt.Sprintf("Round %d decision: %s", ev.Round, oneLiner)
		}
		if id := str("action_id"); id != "" {
			return fmt.Sprintf("Round %d decision: action %s", ev.Round, id)
		}
	case models.EventDiscussion:
		if text := str("text"); text != "" {
			return fmt.Sprintf("Round %d said: %s", ev.Round, text)
		}
	case models.EventBranchEvent:
		if text := str("text"); text != "" {
			return fmt.Sprintf("Round %d turn: %s", ev.Round, text)
		}
	case models.EventResolution:
		if agreement, ok := p["agreement"]; ok {
			return fmt.Sprintf("Round %d resolved with agreement score %v", ev.Round, agreement)
		}
		return fmt.Sprintf("Round %d resolved", ev.Round)
	case models.EventMatchEnd:
		if outcome := str("outcome"); outcome != "" {
			return fmt.Sprintf("Match ended at round %d with outcome %s", ev.Round, outcome)
		}
	}
	return ""
}

func memoryKind(eventType string) models.MemoryKind {
	switch eventType {
	case models.EventDiscussion:
		return models.MemorySocialInteraction
	case models.EventBranchEvent, models.EventResolution:
		return models.MemoryGameEvent
	case models.EventMatchEnd:
		return models.MemoryQuietObservation
	default:
		return models.MemoryGameEvent
	}
}

func strength(eventType string) float64 {
	switch eventType {
	case models.EventMatchEnd:
		return 1
	case models.EventDecision, models.EventBranchEvent:
		return 0.8
	default:
		return 0.5
	}
}

// draftRelationships derives directional suggestions from public discussion
// only. A quiet match produces no drafts rather than a fabricated friendship.
func draftRelationships(view models.SessionView, events []models.Event) []RelationshipUpdate {
	real := make([]models.Seat, 0, len(view.Seats))
	for _, s := range view.Seats {
		if !s.Official && s.CompanionID != "" {
			real = append(real, s)
		}
	}
	out := make([]RelationshipUpdate, 0)
	seen := make(map[string]bool)
	for _, ev := range events {
		if ev.Type != models.EventDiscussion || ev.Visibility != models.VisibilityPublic {
			continue
		}
		from, ok := findSeat(real, ev.SeatID)
		if !ok {
			continue
		}
		for _, to := range real {
			if to.SeatID == from.SeatID {
				continue
			}
			key := from.CompanionID + ">" + to.CompanionID
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, RelationshipUpdate{
				FromCompanionID: from.CompanionID, ToCompanionID: to.CompanionID,
				Direction:      "toward",
				Reason:         fmt.Sprintf("shared a public discussion in round %d (event %d)", ev.Round, ev.ID),
				SourceEventID:  ev.ID,
				SuggestedDelta: map[string]float64{"rapport": 0.1},
				Draft:          true,
			})
			if len(out) >= MaxRelationshipDrafts {
				return out
			}
		}
	}
	return out
}

func findSeat(seats []models.Seat, seatID string) (models.Seat, bool) {
	for _, s := range seats {
		if s.SeatID == seatID {
			return s, true
		}
	}
	return models.Seat{}, false
}

// unresolvedThreads quotes final metric values. It never claims a story fact
// that the session did not record.
func unresolvedThreads(view models.SessionView) []string {
	keys := make([]string, 0, len(view.Metrics))
	for k := range view.Metrics {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, MaxUnresolvedThreads)
	for _, k := range keys {
		if len(out) >= MaxUnresolvedThreads {
			break
		}
		m := view.Metrics[k]
		label := m.Label
		if label == "" {
			label = k
		}
		out = append(out, fmt.Sprintf("%s finished at %.0f/%.0f after an outcome of %q",
			label, m.Value, m.Max, view.Outcome))
	}
	return out
}

// ForRecipient narrows a document to one companion's own memories plus shared
// facts. Relationship drafts remain visible to all participants because they
// are proposals about the group, not a private memory.
func (d Document) ForRecipient(companionID string) Document {
	out := d
	out.RecipientCompanionID = companionID
	out.Memories = make([]models.Memory, 0, len(d.Memories))
	for _, mem := range d.Memories {
		if mem.CompanionID == companionID {
			out.Memories = append(out.Memories, mem)
		}
	}
	return out
}

// Send delivers one follow-up PM per real owner. Official backfill seats have
// no owner to notify. Re-sending for the same session is a no-op.
func (s *Sender) Send(ctx context.Context, view models.SessionView, doc Document) ([]Delivery, error) {
	if view.Status != models.SessionFinished {
		return nil, fail(409, "SESSION_NOT_FINISHED", "session %s is not finished", view.SessionID)
	}
	if s.Port == nil {
		return nil, fail(503, "EIGENFLUX_PORT_UNCONFIGURED", "no EigenFluxPort configured on this Hub")
	}
	if s.HubAgentID == "" {
		return nil, fail(503, "HUB_AGENT_UNCONFIGURED", "HUB_AGENT_ID is required to send follow-ups")
	}

	s.mu.Lock()
	existing := append([]Delivery(nil), s.history[view.SessionID]...)
	s.mu.Unlock()
	already := make(map[string]bool, len(existing))
	for _, d := range existing {
		already[d.AgentID] = true
	}

	pending := make([]models.Seat, 0, len(view.Seats))
	for _, seat := range view.Seats {
		if seat.Official || seat.AgentID == "" || already[seat.AgentID] {
			continue
		}
		pending = append(pending, seat)
	}
	if len(pending) == 0 && len(existing) > 0 {
		return existing, nil
	}

	newDeliveries := make([]Delivery, 0, len(pending))
	for _, seat := range pending {
		recipient := doc.ForRecipient(seat.CompanionID)
		body, err := json.Marshal(recipient)
		if err != nil {
			return nil, fail(500, "FOLLOWUP_ENCODE_FAILED", "%v", err)
		}
		msgID, err := s.Port.SendPM(ctx, s.HubAgentID, seat.AgentID, body)
		if err != nil {
			return nil, fail(502, "FOLLOWUP_PM_FAILED", "to %s: %v", seat.AgentID, err)
		}
		newDeliveries = append(newDeliveries, Delivery{
			AgentID: seat.AgentID, CompanionID: seat.CompanionID,
			MessageID: msgID, MemoryCount: len(recipient.Memories),
			Relationship: len(recipient.RelationshipUpdates),
			Card:         s.projectCard(ctx, view, seat, recipient.GeneratedAt),
		})
	}

	s.mu.Lock()
	// Re-check under the lock: two concurrent sends may have raced. The port's
	// request_id dedup makes the duplicate HTTP call harmless, but the record
	// must still contain one delivery per Agent.
	for _, d := range newDeliveries {
		found := false
		for _, old := range s.history[view.SessionID] {
			if old.AgentID == d.AgentID {
				found = true
				break
			}
		}
		if !found {
			if s.history == nil {
				s.history = make(map[string][]Delivery)
			}
			s.history[view.SessionID] = append(s.history[view.SessionID], d)
		}
	}
	out := append([]Delivery(nil), s.history[view.SessionID]...)
	s.mu.Unlock()
	return out, nil
}

// projectCard fills the configured Agent Card fields for one recipient and
// records the outcome. It never affects the PM delivery: a projection failure is
// reported in the delivery record, not raised, because losing the follow-up
// message over a card write would be the worse failure.
//
// Returns nil when projection is not configured, so an unconfigured Hub makes
// no claim about the card at all.
func (s *Sender) projectCard(ctx context.Context, view models.SessionView, seat models.Seat, generatedAt int64) *CardProjectionRecord {
	if len(s.CardFields) == 0 {
		return nil
	}
	projector, ok := s.Port.(services.CardProjector)
	if !ok {
		return &CardProjectionRecord{Error: "port does not implement Agent Card projection"}
	}
	candidates := make(map[string]string, len(s.CardFields))
	summary := cardSummary(view, generatedAt)
	for _, field := range s.CardFields {
		candidates[field] = summary
	}
	projection, err := projector.ProjectAgentCardFields(ctx, seat.AgentID, candidates)
	if err != nil {
		return &CardProjectionRecord{Error: err.Error()}
	}
	return &CardProjectionRecord{Written: projection.Written, Skipped: projection.Skipped}
}

// cardSummary is the only text the Hub may project. It states the match facts
// the Hub itself produced, with the date, and nothing about personality,
// memories, relationships or motives.
func cardSummary(view models.SessionView, at int64) string {
	day := time.UnixMilli(at).UTC().Format("2006-01-02")
	return fmt.Sprintf("FluxSpace: completed a %d-round deduction theatre on %s (outcome: %s).",
		view.Round, day, view.Outcome)
}
