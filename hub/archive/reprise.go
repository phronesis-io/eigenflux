package archive

import (
	"encoding/json"
	"fmt"

	"fluxspace.ai/hub/models"
)

// Reprise types. Phase 1 implements same_spec only; the other two GDD variants
// swap one variable at a time and need a live model adapter, which does not
// exist yet (ADR-0004 G4 registers that as a labelled deviation).
const (
	RepriseSameSpec         = "same_spec"
	RepriseStatusAuthorized = "authorized"
	RepriseStatusCompleted  = "completed"
)

// SeatDiff is one seat whose action differed in a given round.
type SeatDiff struct {
	SeatID         string `json:"seat_id"`
	OriginalAction string `json:"original_action"`
	RepriseAction  string `json:"reprise_action"`
}

// RoundDiff is one round in which at least one seat diverged.
type RoundDiff struct {
	Round       int        `json:"round"`
	Differences []SeatDiff `json:"differences"`
}

// RepriseComparison answers the question the GDD calls the most valuable replay
// feature: under an identical configuration, where did the decisions diverge?
type RepriseComparison struct {
	OriginalID      string      `json:"original_id"`
	RepriseID       string      `json:"reprise_id"`
	SkeletonHash    string      `json:"skeleton_hash"`
	IdenticalConfig bool        `json:"identical_config"`
	DivergencePoint int         `json:"divergence_point"` // 0 means "never diverged"
	DivergenceSeat  string      `json:"divergence_seat,omitempty"`
	Rounds          []RoundDiff `json:"rounds"`
	OriginalOutcome string      `json:"original_outcome"`
	RepriseOutcome  string      `json:"reprise_outcome"`
	OutcomeChanged  bool        `json:"outcome_changed"`
}

// Reprise is one authorised same-spec re-run of an archived match.
type Reprise struct {
	ID         string             `json:"id"`
	ArchiveID  string             `json:"archive_id"`
	Type       string             `json:"type"`
	Status     string             `json:"status"`
	CreatedAt  int64              `json:"created_at"`
	Consent    PolicyDecision     `json:"consent"`
	Comparison *RepriseComparison `json:"comparison,omitempty"`
	// NewSessionID is set once the reprise has actually been played; the Hub
	// does not create the session itself, because that belongs to the normal
	// invitation path with the same skeleton pinned.
	NewSessionID string `json:"new_session_id,omitempty"`
}

// CompareSameSpec compares two archives that must share the same frozen
// skeleton and seat configuration.
func (a Archive) CompareSameSpec(other Archive) (RepriseComparison, error) {
	if a.ID == other.ID {
		return RepriseComparison{}, fmt.Errorf("cannot compare an archive with itself")
	}
	if err := a.Verify(); err != nil {
		return RepriseComparison{}, fmt.Errorf("original archive: %w", err)
	}
	if err := other.Verify(); err != nil {
		return RepriseComparison{}, fmt.Errorf("reprise archive: %w", err)
	}
	if a.SkeletonHash() != other.SkeletonHash() {
		return RepriseComparison{}, fmt.Errorf(
			"same-spec comparison requires one shared skeleton: %s vs %s",
			a.SkeletonHash(), other.SkeletonHash())
	}
	if err := sameSeatConfiguration(a.Config, other.Config); err != nil {
		return RepriseComparison{}, err
	}

	left := decisionsByRound(a)
	right := decisionsByRound(other)

	rounds := make([]int, 0, len(left))
	for round := range left {
		rounds = append(rounds, round)
	}
	for round := range right {
		if _, ok := left[round]; !ok {
			rounds = append(rounds, round)
		}
	}
	sortInts(rounds)

	cmp := RepriseComparison{
		OriginalID: a.ID, RepriseID: other.ID, SkeletonHash: a.SkeletonHash(),
		IdenticalConfig: true,
		OriginalOutcome: a.Settlement.Outcome, RepriseOutcome: other.Settlement.Outcome,
	}
	cmp.OutcomeChanged = cmp.OriginalOutcome != cmp.RepriseOutcome

	seatIndex := make(map[string]int, len(a.Config.Seats))
	for _, s := range a.Config.Seats {
		seatIndex[s.SeatID] = s.Seat
	}

	for _, round := range rounds {
		seats := make([]string, 0, len(left[round])+len(right[round]))
		for seat := range left[round] {
			seats = append(seats, seat)
		}
		for seat := range right[round] {
			if _, ok := left[round][seat]; !ok {
				seats = append(seats, seat)
			}
		}
		sortSeatsByIndex(seats, seatIndex)

		diff := RoundDiff{Round: round}
		for _, seat := range seats {
			if left[round][seat] == right[round][seat] {
				continue
			}
			diff.Differences = append(diff.Differences, SeatDiff{
				SeatID: seat, OriginalAction: left[round][seat], RepriseAction: right[round][seat],
			})
		}
		if len(diff.Differences) == 0 {
			continue
		}
		roundsWithDiff := diff
		cmp.Rounds = append(cmp.Rounds, roundsWithDiff)
		if cmp.DivergencePoint == 0 {
			cmp.DivergencePoint = round
			cmp.DivergenceSeat = diff.Differences[0].SeatID
		}
	}
	return cmp, nil
}

// decisionsByRound extracts round -> seat -> action_id from decision events.
func decisionsByRound(a Archive) map[int]map[string]string {
	out := make(map[int]map[string]string)
	for _, ev := range a.Events {
		if ev.Type != models.EventDecision {
			continue
		}
		var payload struct {
			SeatID   string `json:"seat_id"`
			ActionID string `json:"action_id"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			continue
		}
		seat := payload.SeatID
		if seat == "" {
			seat = ev.SeatID
		}
		if out[ev.Round] == nil {
			out[ev.Round] = make(map[string]string)
		}
		out[ev.Round][seat] = payload.ActionID
	}
	return out
}

// sameSeatConfiguration enforces the rest of the GDD's "identical ... seat
// configuration" requirement: same number of seats, same companion in each
// seat, same contract, same round count.
func sameSeatConfiguration(a, b Config) error {
	if a.TotalRounds != b.TotalRounds {
		return fmt.Errorf("seat configuration differs: total rounds %d vs %d", a.TotalRounds, b.TotalRounds)
	}
	if a.SocialContract != b.SocialContract {
		return fmt.Errorf("same-spec comparison requires an identical social contract")
	}
	if len(a.Seats) != len(b.Seats) {
		return fmt.Errorf("seat count differs: %d vs %d", len(a.Seats), len(b.Seats))
	}
	for i := range a.Seats {
		if a.Seats[i].Seat != b.Seats[i].Seat || a.Seats[i].CompanionID != b.Seats[i].CompanionID {
			return fmt.Errorf("seat %d holds %q vs %q", i, a.Seats[i].CompanionID, b.Seats[i].CompanionID)
		}
	}
	return nil
}

func sortInts(v []int) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

func sortSeatsByIndex(seats []string, index map[string]int) {
	for i := 1; i < len(seats); i++ {
		for j := i; j > 0 && index[seats[j]] < index[seats[j-1]]; j-- {
			seats[j], seats[j-1] = seats[j-1], seats[j]
		}
	}
}
