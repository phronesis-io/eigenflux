package models

import "encoding/json"

// Wire types for FluxSpace Hub phase 1. Field names and shapes are fixed by
// docs/architecture/fluxspace-contracts.md §3-§5. Do not rename them without
// updating that contract and its golden fixtures.

// Visibility controls which SSE stream may receive an event.
const (
	VisibilityPublic     = "public"
	VisibilityPrivate    = "private"
	VisibilityOmniscient = "omniscient"
)

// EventType enumerates the replay/archive event stream from story 008 AC1.
const (
	EventMatchStart        = "match_start"
	EventRoundBegin        = "round_begin"
	EventInformationReveal = "information_reveal"
	EventDiscussion        = "discussion"
	EventDecision          = "decision"
	EventResolution        = "resolution"
	EventBranchEvent       = "branch_event"
	EventMatchEnd          = "match_end"
	EventPhaseChanged      = "phase_changed"
)

// Phase is one of the four GDD round phases.
type Phase string

const (
	PhaseObserve Phase = "observe"
	PhaseDiscuss Phase = "discuss"
	PhaseAct     Phase = "act"
	PhaseResolve Phase = "resolve"
)

// SessionStatus is the lifecycle of a Hub session.
type SessionStatus string

const (
	SessionLobby    SessionStatus = "lobby"
	SessionLive     SessionStatus = "live"
	SessionFinished SessionStatus = "finished"
)

// Proposal is one of the three generated game proposals in an invitation.
type Proposal struct {
	Index          int    `json:"index"`
	Type           string `json:"type"`
	Seats          int    `json:"seats"`
	Minutes        int    `json:"minutes"`
	StructuralHash string `json:"structural_hash"`
	// Fingerprint is the dedup key for the five-match freshness rule. It must
	// be exposed here: callers feed the fingerprints of completed matches back
	// as invitation.Request.HistoryFingerprints, and dedup is enforced on
	// Fingerprint (not StructuralHash, which deliberately collides), so without
	// this field no client could ever build that history.
	Fingerprint    string   `json:"fingerprint"`
	ContentHash    string   `json:"content_hash"`
	Summary        string   `json:"summary"`
	ContractPoints []string `json:"contract_points"`
}

// SocialContract is a gentleman's agreement: recorded and displayed, never
// system-enforced (GDD "Invitation flow").
type SocialContract struct {
	Sharing         string `json:"sharing"`
	PrivateChannels string `json:"private_channels"`
	ReplayConsent   string `json:"replay_consent"`
	Visibility      string `json:"visibility"`
}

// Invitation is the structured JSON carried in an EigenFlux PM content field.
type Invitation struct {
	Kind                string         `json:"kind"`
	InvitationID        string         `json:"invitation_id"`
	FromAgentID         string         `json:"from_agent_id"`
	FromCompanionID     string         `json:"from_companion_id"`
	ToAgentID           string         `json:"to_agent_id"`
	Proposals           []Proposal     `json:"proposals"`
	SocialContractDraft SocialContract `json:"social_contract_draft"`
	EstimatedMinutes    int            `json:"estimated_minutes"`
	ExpiresAt           int64          `json:"expires_at"`
	SelectedProposal    int            `json:"selected_proposal"`
}

// InvitationReply is the accept/reject/modify answer carried back over PM.
// SelectedProposal is a phase-1 addition: an "accept" must name which of the
// three proposals is being accepted, otherwise a negotiation cannot be closed
// deterministically.
type InvitationReply struct {
	Kind             string   `json:"kind"`
	InvitationID     string   `json:"invitation_id"`
	Action           string   `json:"action"` // accept | reject | modify
	AgentID          string   `json:"agent_id"`
	SelectedProposal int      `json:"selected_proposal,omitempty"`
	Amendments       []string `json:"amendments,omitempty"`
}

// Wire kinds for the structured PM documents (contracts §4).
const (
	InvitationKind = "fluxspace.invitation.v1"
	ReplyKind      = "fluxspace.invitation.reply.v1"
	FollowUpKind   = "fluxspace.followup.v1"
)

// Reply actions.
const (
	ReplyAccept = "accept"
	ReplyReject = "reject"
	ReplyModify = "modify"
)

// SeatClaim is one confirmed participant at session creation. OwnerID is the
// EigenFlux Agent owner: two companions of one owner can never share a match.
type SeatClaim struct {
	SeatID      string      `json:"seat_id"`
	OwnerID     string      `json:"owner_id"`
	AgentID     string      `json:"agent_id"`
	CompanionID string      `json:"companion_id"`
	DisplayName string      `json:"display_name"`
	Official    bool        `json:"official"`
	Personality Personality `json:"personality"`
}

// Seat is a bound seat inside a live session.
type Seat struct {
	SeatClaim
	Joined bool `json:"joined"`
}

// DecisionSubmission matches the JSON Schema in contracts §5: exactly three
// fields, action_id must exist in the current round's action pool.
type DecisionSubmission struct {
	ActionID         string  `json:"action_id"`
	ReasoningSummary string  `json:"reasoning_summary"`
	Confidence       float64 `json:"confidence"`
	Source           string  `json:"source,omitempty"`
}

// DiscussionMessage is one bounded discussion line (GDD: 2 per seat per round,
// 200 characters each, two rounds).
type DiscussionMessage struct {
	SeatID  string `json:"seat_id"`
	Text    string `json:"text"`
	Channel string `json:"channel"`
}

// DensitySummaries are the three-density decision summaries from story 008 AC2.
// The Hub records placeholders that are always truthful: the rule engine
// exposes its scoring evidence, so no summary invents model reasoning.
type DensitySummaries struct {
	OneLiner   string `json:"one_liner"`
	Structured string `json:"structured"`
	Comparison string `json:"comparison"`
}

// Event is one SSE data frame. ID is monotonic per session; reconnect uses
// Last-Event-ID plus the snapshot endpoint.
type Event struct {
	ID            int64           `json:"id"`
	Type          string          `json:"type"`
	SessionID     string          `json:"session_id"`
	Round         int             `json:"round"`
	Phase         Phase           `json:"phase"`
	SeatID        string          `json:"seat_id,omitempty"`
	Visibility    string          `json:"visibility"`
	Payload       json.RawMessage `json:"payload"`
	StateSnapshot json.RawMessage `json:"state_snapshot"`
	TS            int64           `json:"ts"`
}

// SessionView is the state query and snapshot response. It deliberately omits
// private discussion content: that belongs to the filtered SSE stream.
type SessionView struct {
	SessionID       string              `json:"session_id"`
	InvitationID    string              `json:"invitation_id"`
	Status          SessionStatus       `json:"status"`
	Round           int                 `json:"round"`
	TotalRounds     int                 `json:"total_rounds"`
	Phase           Phase               `json:"phase"`
	Seats           []Seat              `json:"seats"`
	Metrics         map[string]Metric   `json:"metrics"`
	Outcome         string              `json:"outcome,omitempty"`
	Skeleton        *Skeleton           `json:"skeleton"`
	Decisions       map[string]Decision `json:"decisions"`
	LastEventID     int64               `json:"last_event_id"`
	TalkCounts      map[string]int      `json:"talk_counts"`
	SocialContract  SocialContract      `json:"social_contract"`
	OfficialSeatIDs []string            `json:"official_seat_ids"`
}
