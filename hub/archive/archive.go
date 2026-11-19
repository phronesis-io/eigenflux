// Package archive turns a finished Hub session into an immutable, verifiable,
// event-sourced replay archive (GDD "Replay system", S4 exit criteria:
// SHA-256 archive verification, six viewing modes, all-participant consent).
//
// Two rules shape the design:
//
//  1. An archive is frozen. Everything that defines the match is captured once,
//     at Build time, and every hash is computed over that capture. Nothing in
//     this package mutates an Archive after Build; publication state and
//     consent live in Store, precisely so that publishing cannot change the
//     bytes an integrity check covers.
//  2. Nothing is invented. Fields the Hub genuinely does not know (persona id,
//     model version) are left empty AND listed in Integrity.MissingFields
//     rather than being filled with a plausible default. A replay that claims a
//     model version it never had is worse than one that admits ignorance.
package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"fluxspace.ai/hub/followup"
	"fluxspace.ai/hub/models"
)

// Version is the archive document version. It participates in ContentHash, so
// changing the layout without bumping it would silently reuse old hashes.
const Version = "1.0"

// Kind identifies the wire document.
const Kind = "fluxspace.replay.v1"

// SeatConfig is one frozen seat in the match configuration. The GDD archive
// layout asks for companion_id, persona_id, model_version and a full
// persona_snapshot per seat; the Hub only owns some of those, so the rest are
// carried when a caller supplies them and otherwise reported as missing.
type SeatConfig struct {
	Seat            int             `json:"seat"`
	SeatID          string          `json:"seat_id"`
	CompanionID     string          `json:"companion_id"`
	OwnerID         string          `json:"owner_id"`
	AgentID         string          `json:"agent_id"`
	DisplayName     string          `json:"display_name"`
	Official        bool            `json:"official"`
	PersonaID       string          `json:"persona_id,omitempty"`
	ModelVersion    string          `json:"model_version,omitempty"`
	PersonaSnapshot json.RawMessage `json:"persona_snapshot"`
	// PersonaSource records where PersonaSnapshot came from: "hub_seat_claim"
	// when it is the six-dimension personality the seat claim carried, or
	// "client_supplied" when the archiving caller enriched it.
	PersonaSource string `json:"persona_source"`
}

// Config is the immutable match configuration. It is compared field-for-field
// by the same-spec reprise check, and the skeleton inside it is what the
// skeleton hash covers.
type Config struct {
	Skeleton       *models.Skeleton      `json:"skeleton"`
	SocialContract models.SocialContract `json:"social_contract"`
	Seats          []SeatConfig          `json:"seats"`
	TotalRounds    int                   `json:"total_rounds"`
}

// Settlement is the post-match record. Memories and relationship drafts are the
// ones the follow-up pass actually derived from these events, not a second,
// independently invented derivation.
type Settlement struct {
	Outcome            string                        `json:"outcome"`
	FinalMetrics       map[string]models.Metric      `json:"final_metrics"`
	RoundsPlayed       int                           `json:"rounds_played"`
	Memories           []models.Memory               `json:"memories"`
	RelationshipDrafts []followup.RelationshipUpdate `json:"relationship_drafts"`
	UnresolvedThreads  []string                      `json:"unresolved_threads"`
}

// Integrity is the verification block the S4 exit criterion asks for.
type Integrity struct {
	EventCount    int      `json:"event_count"`
	ContentHash   string   `json:"content_hash"`
	SkeletonHash  string   `json:"skeleton_hash"`
	Verified      bool     `json:"verified"`
	Complete      bool     `json:"complete"`
	MissingFields []string `json:"missing_fields"`
}

// Archive is one immutable match archive.
type Archive struct {
	Kind       string         `json:"kind"`
	ID         string         `json:"id"`
	SessionID  string         `json:"session_id"`
	Version    string         `json:"version"`
	CreatedAt  int64          `json:"created_at"`
	Config     Config         `json:"config"`
	Events     []models.Event `json:"events"`
	Settlement Settlement     `json:"settlement"`
	Integrity  Integrity      `json:"integrity"`
}

// Options are the optional enrichments a caller may supply at Build time.
type Options struct {
	// ID defaults to a hash-derived identifier so rebuilding an archive from the
	// same session yields the same id.
	ID string
	// Now is the archive timestamp; the Manager's clock is not reachable here.
	Now int64
	// Seats enriches the seat configuration by SeatID. PersonaID, ModelVersion
	// and a client-side persona snapshot can only arrive this way.
	Seats []SeatConfig
	// FollowUps are the derived follow-up documents for this session. Their
	// memories and relationship drafts become the settlement record.
	FollowUps []followup.Document
}

// Build freezes a finished session into an archive.
//
// It refuses to archive anything that is not a completed match: an "immutable"
// record of a half-played game is a contradiction, and the replay modes all
// assume a terminal match_end event.
func Build(view models.SessionView, events []models.Event, opts Options) (Archive, error) {
	if view.Status != models.SessionFinished {
		return Archive{}, fmt.Errorf("archive requires a finished session, got %q", view.Status)
	}
	if view.Skeleton == nil {
		return Archive{}, fmt.Errorf("archive requires the session skeleton")
	}
	if len(events) == 0 {
		return Archive{}, fmt.Errorf("archive requires the retained event stream")
	}
	if err := requireTerminalEvents(events); err != nil {
		return Archive{}, err
	}

	seats := buildSeats(view, opts.Seats)
	cfg := Config{
		Skeleton:       view.Skeleton,
		SocialContract: view.SocialContract,
		Seats:          seats,
		TotalRounds:    view.TotalRounds,
	}

	settlement := Settlement{
		Outcome:      view.Outcome,
		FinalMetrics: copyMetrics(view.Metrics),
		RoundsPlayed: view.Round,
	}
	for _, doc := range opts.FollowUps {
		settlement.Memories = append(settlement.Memories, doc.Memories...)
		settlement.RelationshipDrafts = append(settlement.RelationshipDrafts, doc.RelationshipUpdates...)
		settlement.UnresolvedThreads = append(settlement.UnresolvedThreads, doc.UnresolvedThreads...)
	}
	settlement.UnresolvedThreads = dedupeStrings(settlement.UnresolvedThreads)

	archived := make([]models.Event, len(events))
	copy(archived, events)
	for i := range archived {
		archived[i].Payload = append(json.RawMessage(nil), events[i].Payload...)
		archived[i].StateSnapshot = append(json.RawMessage(nil), events[i].StateSnapshot...)
	}

	a := Archive{
		Kind:       Kind,
		SessionID:  view.SessionID,
		Version:    Version,
		CreatedAt:  opts.Now,
		Config:     cfg,
		Events:     archived,
		Settlement: settlement,
	}
	a.ID = opts.ID
	content, err := a.contentHash()
	if err != nil {
		return Archive{}, err
	}
	skeleton, err := a.skeletonHash()
	if err != nil {
		return Archive{}, err
	}
	if a.ID == "" {
		a.ID = "replay_" + content[len("sha256:"):len("sha256:")+12]
	}
	a.Integrity = Integrity{
		EventCount:    len(a.Events),
		ContentHash:   content,
		SkeletonHash:  skeleton,
		Verified:      true,
		Complete:      len(missingFields(cfg)) == 0,
		MissingFields: missingFields(cfg),
	}
	return a, nil
}

// Verify recomputes both hashes and the event count from the archive's own
// contents. It is the check a store must run before trusting bytes that came
// from disk, a network peer, or another process.
func (a Archive) Verify() error {
	if a.Kind != Kind {
		return fmt.Errorf("not a replay archive: kind %q", a.Kind)
	}
	if a.Version != Version {
		return fmt.Errorf("unsupported archive version %q, want %q", a.Version, Version)
	}
	content, err := a.contentHash()
	if err != nil {
		return err
	}
	if content != a.Integrity.ContentHash {
		return fmt.Errorf("content hash mismatch: recorded %s, recomputed %s",
			a.Integrity.ContentHash, content)
	}
	skeleton, err := a.skeletonHash()
	if err != nil {
		return err
	}
	if skeleton != a.Integrity.SkeletonHash {
		return fmt.Errorf("skeleton hash mismatch: recorded %s, recomputed %s",
			a.Integrity.SkeletonHash, skeleton)
	}
	if len(a.Events) != a.Integrity.EventCount {
		return fmt.Errorf("event count mismatch: recorded %d, found %d",
			a.Integrity.EventCount, len(a.Events))
	}
	if !a.Integrity.Verified {
		return fmt.Errorf("archive is marked unverified")
	}
	return nil
}

// Load decodes an archive and refuses to return one that does not verify. There
// is no "load anyway" variant: an unverifiable archive is exactly the failure
// mode this block exists to catch.
func Load(data []byte) (Archive, error) {
	var a Archive
	if err := json.Unmarshal(data, &a); err != nil {
		return Archive{}, fmt.Errorf("decode archive: %w", err)
	}
	if err := a.Verify(); err != nil {
		return Archive{}, err
	}
	return a, nil
}

// Marshal serialises the archive for storage or transfer.
func (a Archive) Marshal() ([]byte, error) {
	return json.Marshal(a)
}

// ContentHash returns the recorded hash, for callers that only display it.
func (a Archive) ContentHash() string { return a.Integrity.ContentHash }

// SkeletonHash returns the frozen skeleton hash, which is what the same-spec
// reprise comparison keys on.
func (a Archive) SkeletonHash() string { return a.Integrity.SkeletonHash }

// contentHash covers the immutable capture: version, configuration, event
// stream and settlement. Publication state is deliberately excluded - it lives
// in Store - so publishing never invalidates a hash.
func (a Archive) contentHash() (string, error) {
	payload, err := json.Marshal(struct {
		Version    string         `json:"version"`
		SessionID  string         `json:"session_id"`
		Config     Config         `json:"config"`
		Events     []models.Event `json:"events"`
		Settlement Settlement     `json:"settlement"`
	}{a.Version, a.SessionID, a.Config, a.Events, a.Settlement})
	if err != nil {
		return "", fmt.Errorf("hash archive: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// skeletonHash covers only the skeleton, so two reprises of the same game share
// it while their event streams diverge.
func (a Archive) skeletonHash() (string, error) {
	if a.Config.Skeleton == nil {
		return "", fmt.Errorf("archive has no skeleton")
	}
	payload, err := json.Marshal(a.Config.Skeleton)
	if err != nil {
		return "", fmt.Errorf("hash skeleton: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// requireTerminalEvents enforces the event envelope the six viewing modes
// assume: exactly one match_start and exactly one match_end, the latter last.
func requireTerminalEvents(events []models.Event) error {
	starts, ends := 0, 0
	for i, ev := range events {
		switch ev.Type {
		case models.EventMatchStart:
			starts++
		case models.EventMatchEnd:
			ends++
			if i != len(events)-1 {
				return fmt.Errorf("match_end at position %d of %d: archive requires it to be the final event",
					i, len(events))
			}
		}
	}
	if starts != 1 || ends != 1 {
		return fmt.Errorf("archive requires exactly one match_start and one match_end, found %d and %d",
			starts, ends)
	}
	return nil
}

// buildSeats merges the Hub's own seat claims with any caller enrichment.
func buildSeats(view models.SessionView, supplied []SeatConfig) []SeatConfig {
	bySeat := make(map[string]SeatConfig, len(supplied))
	for _, s := range supplied {
		bySeat[s.SeatID] = s
	}
	out := make([]SeatConfig, 0, len(view.Seats))
	for i, seat := range view.Seats {
		persona, err := json.Marshal(seat.Personality)
		if err != nil {
			persona = json.RawMessage("null")
		}
		cfg := SeatConfig{
			Seat:            i,
			SeatID:          seat.SeatID,
			CompanionID:     seat.CompanionID,
			OwnerID:         seat.OwnerID,
			AgentID:         seat.AgentID,
			DisplayName:     seat.DisplayName,
			Official:        seat.Official,
			PersonaSnapshot: persona,
			PersonaSource:   "hub_seat_claim",
		}
		if extra, ok := bySeat[seat.SeatID]; ok {
			if extra.PersonaID != "" {
				cfg.PersonaID = extra.PersonaID
			}
			if extra.ModelVersion != "" {
				cfg.ModelVersion = extra.ModelVersion
			}
			if len(extra.PersonaSnapshot) > 0 && string(extra.PersonaSnapshot) != "null" {
				cfg.PersonaSnapshot = extra.PersonaSnapshot
				cfg.PersonaSource = "client_supplied"
			}
		}
		out = append(out, cfg)
	}
	return out
}

// missingFields names what the archive could not capture, instead of leaving a
// silent blank that a reader might mistake for "not applicable".
func missingFields(cfg Config) []string {
	var missing []string
	if len(cfg.Seats) == 0 {
		return []string{"config.seats"}
	}
	for _, seat := range cfg.Seats {
		if seat.PersonaID == "" {
			missing = append(missing, fmt.Sprintf("config.seats[%d].persona_id", seat.Seat))
		}
		if seat.ModelVersion == "" {
			missing = append(missing, fmt.Sprintf("config.seats[%d].model_version", seat.Seat))
		}
		if len(seat.PersonaSnapshot) == 0 || string(seat.PersonaSnapshot) == "null" {
			missing = append(missing, fmt.Sprintf("config.seats[%d].persona_snapshot", seat.Seat))
		}
	}
	sort.Strings(missing)
	return missing
}

func copyMetrics(in map[string]models.Metric) map[string]models.Metric {
	out := make(map[string]models.Metric, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
