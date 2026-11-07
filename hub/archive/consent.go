package archive

import (
	"fmt"
	"sort"
	"sync"
)

// Replay consent policies from the GDD social contract
// (`spectators.replay_consent: all | majority | none`).
const (
	PolicyAll      = "all"
	PolicyMajority = "majority"
	PolicyNone     = "none"
)

// Grant is one participant owner's decision about publishing an archive.
type Grant struct {
	ArchiveID   string `json:"archive_id"`
	CompanionID string `json:"companion_id"`
	OwnerID     string `json:"owner_id"`
	Accepted    bool   `json:"accepted"`
	At          int64  `json:"at"`
}

// PolicyDecision is the evaluated outcome of the consent ledger for one
// archive. It names exactly who is missing, so a refusal is actionable instead
// of just "denied".
type PolicyDecision struct {
	Policy       string   `json:"policy"`
	Participants []string `json:"participants"`
	Granted      []string `json:"granted"`
	Missing      []string `json:"missing"`
	Public       bool     `json:"public"`
	Reason       string   `json:"reason"`
}

// Ledger records participant consent per archive. Consent is owner-side data
// that the Hub only ever receives, never infers.
type Ledger struct {
	mu     sync.Mutex
	grants map[string]map[string]Grant // archiveID -> companionID -> grant
}

// NewLedger returns an empty ledger.
func NewLedger() *Ledger {
	return &Ledger{grants: make(map[string]map[string]Grant)}
}

// Record stores one participant's answer, overwriting any earlier one so an
// owner can change their mind before publication.
func (l *Ledger) Record(archiveID, companionID, ownerID string, accepted bool, at int64) error {
	if archiveID == "" || companionID == "" {
		return fmt.Errorf("consent requires archive_id and companion_id")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.grants[archiveID] == nil {
		l.grants[archiveID] = make(map[string]Grant)
	}
	l.grants[archiveID][companionID] = Grant{
		ArchiveID: archiveID, CompanionID: companionID, OwnerID: ownerID,
		Accepted: accepted, At: at,
	}
	return nil
}

// Grants returns a copy of every recorded grant for an archive.
func (l *Ledger) Grants(archiveID string) []Grant {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Grant, 0, len(l.grants[archiveID]))
	for _, g := range l.grants[archiveID] {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CompanionID < out[j].CompanionID })
	return out
}

// Evaluate applies the archive's own replay_consent policy.
//
// Fail-closed by construction: an empty policy (an invitation that never
// specified one) is treated as "all", and an unrecognised policy is an error
// rather than a permissive default.
func (l *Ledger) Evaluate(a Archive) (PolicyDecision, error) {
	policy := a.Config.SocialContract.ReplayConsent
	if policy == "" {
		policy = PolicyAll
	}
	switch policy {
	case PolicyAll, PolicyMajority, PolicyNone:
	default:
		return PolicyDecision{}, fmt.Errorf("unknown replay_consent policy %q", policy)
	}

	participants := a.SigningParticipants()
	if len(participants) == 0 {
		// An official-backfill-only match has no owner to ask. Policy is then
		// satisfied vacuously; say so instead of pretending consent was sought.
		return PolicyDecision{
			Policy: policy, Participants: nil, Granted: nil, Missing: nil,
			Public: policy != PolicyNone,
			Reason: "no non-official participants; policy satisfied vacuously",
		}, nil
	}

	l.mu.Lock()
	recorded := make(map[string]Grant, len(l.grants[a.ID]))
	for k, v := range l.grants[a.ID] {
		recorded[k] = v
	}
	l.mu.Unlock()

	granted := make([]string, 0, len(participants))
	missing := make([]string, 0, len(participants))
	for _, companion := range participants {
		if g, ok := recorded[companion]; ok && g.Accepted {
			granted = append(granted, companion)
			continue
		}
		missing = append(missing, companion)
	}

	decision := PolicyDecision{
		Policy: policy, Participants: participants, Granted: granted, Missing: missing,
	}
	switch policy {
	case PolicyNone:
		decision.Public = false
		decision.Reason = "policy is none: this archive may never be published"
	case PolicyAll:
		decision.Public = len(missing) == 0
		if decision.Public {
			decision.Reason = fmt.Sprintf("all %d participant(s) consented", len(participants))
		} else {
			decision.Reason = fmt.Sprintf("%d participant(s) have not consented", len(missing))
		}
	case PolicyMajority:
		// Strictly more than half, so a two-seat match still needs both owners.
		decision.Public = len(granted)*2 > len(participants)
		decision.Reason = fmt.Sprintf("%d of %d participant(s) consented; majority requires more than half",
			len(granted), len(participants))
	}
	return decision, nil
}

// RequireAllForReprise is the same-spec reprise gate. The GDD demands "all
// original participants' owners consent" for a reprise regardless of the
// archive's publication policy, so this intentionally ignores that policy.
func (l *Ledger) RequireAllForReprise(a Archive) (PolicyDecision, error) {
	participants := a.SigningParticipants()
	l.mu.Lock()
	recorded := make(map[string]Grant, len(l.grants[a.ID]))
	for k, v := range l.grants[a.ID] {
		recorded[k] = v
	}
	l.mu.Unlock()

	granted := make([]string, 0, len(participants))
	missing := make([]string, 0, len(participants))
	for _, companion := range participants {
		if g, ok := recorded[companion]; ok && g.Accepted {
			granted = append(granted, companion)
			continue
		}
		missing = append(missing, companion)
	}
	decision := PolicyDecision{
		Policy: PolicyAll, Participants: participants, Granted: granted, Missing: missing,
		Public: len(missing) == 0,
	}
	if decision.Public {
		decision.Reason = fmt.Sprintf("all %d participant(s) consented to a reprise", len(participants))
	} else {
		decision.Reason = fmt.Sprintf("reprise needs every participant; %d still missing", len(missing))
	}
	return decision, nil
}

// SigningParticipants lists the companion ids whose owners must consent, in
// seat order. Official backfills have no owner and are excluded: asking a
// placeholder for permission would be theatre, not consent.
func (a Archive) SigningParticipants() []string {
	seen := make(map[string]bool, len(a.Config.Seats))
	out := make([]string, 0, len(a.Config.Seats))
	for _, s := range a.Config.Seats {
		if s.Official || s.CompanionID == "" || seen[s.CompanionID] {
			continue
		}
		seen[s.CompanionID] = true
		out = append(out, s.CompanionID)
	}
	return out
}
