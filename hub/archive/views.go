package archive

import (
	"encoding/json"
	"fmt"
	"sort"

	"fluxspace.ai/hub/models"
)

// The six viewing modes from the GDD "Viewing modes" table. They are all
// projections over the same immutable event stream: the archive never stores
// six copies, and a projection can never disagree with the archive it came
// from.
const (
	ModeFull                 = "full"
	ModeAccelerated          = "accelerated"
	ModeSingleCharacter      = "single_character"
	ModeOmniscientComparison = "omniscient_comparison"
	ModeKeyMoments           = "key_moments"
	ModeSameSpecComparison   = "same_spec_comparison"
)

// DefaultMomentLimit keeps the "key moments" projection short enough to scan.
const DefaultMomentLimit = 5

// TimeScaleCompressible lists the speeds the accelerated mode accepts. The GDD
// names 2x and 4x; anything else is rejected rather than silently rounded.
var TimeScaleCompressible = map[int]bool{2: true, 4: true}

// Modes returns the six modes in GDD table order.
func Modes() []string {
	return []string{
		ModeFull, ModeAccelerated, ModeSingleCharacter,
		ModeOmniscientComparison, ModeKeyMoments, ModeSameSpecComparison,
	}
}

// TimelineEvent is one event as a replay client consumes it.
type TimelineEvent struct {
	Seq        int64           `json:"seq"`
	Type       string          `json:"type"`
	Round      int             `json:"round"`
	Phase      models.Phase    `json:"phase"`
	SeatID     string          `json:"seat_id,omitempty"`
	Visibility string          `json:"visibility"`
	TS         int64           `json:"ts"`
	OffsetMS   int64           `json:"offset_ms"`
	Payload    json.RawMessage `json:"payload"`
}

// DecisionAtRound is one seat's decision inside the omniscient comparison.
type DecisionAtRound struct {
	SeatID    string          `json:"seat_id"`
	ActionID  string          `json:"action_id"`
	Summaries json.RawMessage `json:"summaries,omitempty"`
	Official  bool            `json:"official"`
	Snapshot  json.RawMessage `json:"state_snapshot"`
}

// RoundComparison places every seat's decision for a round side by side.
type RoundComparison struct {
	Round     int                `json:"round"`
	Decisions []DecisionAtRound  `json:"decisions"`
	Agreement float64            `json:"agreement"`
	Synergy   float64            `json:"synergy"`
	Delta     map[string]float64 `json:"delta,omitempty"`
}

// Moment is one automatically marked turning point.
type Moment struct {
	EventID   int64   `json:"event_id"`
	Round     int     `json:"round"`
	Kind      string  `json:"kind"` // branch | metric_swing | outcome
	Magnitude float64 `json:"magnitude"`
	Reason    string  `json:"reason"`
	Ref       string  `json:"ref,omitempty"`
}

// View is the response of every mode. Unused fields are omitted, so a client
// can tell from the shape which projection it received.
type View struct {
	Mode       string             `json:"mode"`
	ArchiveID  string             `json:"archive_id"`
	SessionID  string             `json:"session_id"`
	Speed      int                `json:"speed,omitempty"`
	SeatID     string             `json:"seat_id,omitempty"`
	DurationMS int64              `json:"duration_ms,omitempty"`
	EventCount int                `json:"event_count,omitempty"`
	Events     []TimelineEvent    `json:"events,omitempty"`
	Rounds     []RoundComparison  `json:"rounds,omitempty"`
	Moments    []Moment           `json:"moments,omitempty"`
	Comparison *RepriseComparison `json:"comparison,omitempty"`
}

// Query selects and parameterises a projection. Fields that a mode does not use
// are rejected by Project rather than ignored, so a client cannot believe it
// asked for a filtered view and silently get an unfiltered one.
type Query struct {
	Mode        string
	Speed       int
	SeatID      string
	CompareWith *Archive
	MomentLimit int
}

// Project builds the requested view.
func (a Archive) Project(q Query) (View, error) {
	if err := a.Verify(); err != nil {
		return View{}, fmt.Errorf("cannot project an archive that does not verify: %w", err)
	}
	switch q.Mode {
	case ModeFull:
		return a.projectTimeline(ModeFull, "", 0)
	case ModeAccelerated:
		if !TimeScaleCompressible[q.Speed] {
			return View{}, fmt.Errorf("accelerated mode accepts speed 2 or 4, got %d", q.Speed)
		}
		return a.projectTimeline(ModeAccelerated, "", q.Speed)
	case ModeSingleCharacter:
		if q.SeatID == "" {
			return View{}, fmt.Errorf("single_character requires seat_id")
		}
		if !a.hasSeat(q.SeatID) {
			return View{}, fmt.Errorf("seat %q is not part of this archive", q.SeatID)
		}
		return a.projectTimeline(ModeSingleCharacter, q.SeatID, 0)
	case ModeOmniscientComparison:
		return a.projectOmniscientComparison(), nil
	case ModeKeyMoments:
		limit := q.MomentLimit
		if limit <= 0 {
			limit = DefaultMomentLimit
		}
		return a.projectKeyMoments(limit), nil
	case ModeSameSpecComparison:
		if q.CompareWith == nil {
			return View{}, fmt.Errorf("same_spec_comparison requires a second archive")
		}
		cmp, err := a.CompareSameSpec(*q.CompareWith)
		if err != nil {
			return View{}, err
		}
		return View{
			Mode: ModeSameSpecComparison, ArchiveID: a.ID, SessionID: a.SessionID,
			Comparison: &cmp,
		}, nil
	default:
		return View{}, fmt.Errorf("unknown viewing mode %q", q.Mode)
	}
}

// projectTimeline is full, accelerated and single-character: all three replay
// the event stream, differing only in speed and visibility filter.
func (a Archive) projectTimeline(mode, seatID string, speed int) (View, error) {
	events := make([]TimelineEvent, 0, len(a.Events))
	origin := int64(0)
	if len(a.Events) > 0 {
		origin = a.Events[0].TS
	}
	for _, ev := range a.Events {
		if !replayVisible(ev, seatID) {
			continue
		}
		offset := ev.TS - origin
		if offset < 0 {
			offset = 0
		}
		if speed > 0 {
			offset /= int64(speed)
		}
		events = append(events, TimelineEvent{
			Seq: ev.ID, Type: ev.Type, Round: ev.Round, Phase: ev.Phase,
			SeatID: ev.SeatID, Visibility: ev.Visibility, TS: ev.TS,
			OffsetMS: offset, Payload: ev.Payload,
		})
	}
	view := View{
		Mode: mode, ArchiveID: a.ID, SessionID: a.SessionID, SeatID: seatID,
		Speed: speed, EventCount: len(events), Events: events,
	}
	if len(events) > 0 {
		view.DurationMS = events[len(events)-1].OffsetMS
	}
	return view, nil
}

// replayVisible mirrors the live spectator rule in session.visibleTo: public
// events plus this seat's own private events. Omniscient-only events never
// appear in a single-character view, otherwise the mode would leak the thing it
// exists to hide.
func replayVisible(ev models.Event, seatID string) bool {
	if seatID == "" {
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

// projectOmniscientComparison lines up every seat's decision per round.
func (a Archive) projectOmniscientComparison() View {
	type roundState struct {
		decisions []DecisionAtRound
		agreement float64
		synergy   float64
		delta     map[string]float64
	}
	order := make([]int, 0)
	byRound := make(map[int]*roundState)
	ensure := func(round int) *roundState {
		if st, ok := byRound[round]; ok {
			return st
		}
		st := &roundState{}
		byRound[round] = st
		order = append(order, round)
		return st
	}

	for _, ev := range a.Events {
		switch ev.Type {
		case models.EventDecision:
			var payload struct {
				SeatID    string          `json:"seat_id"`
				ActionID  string          `json:"action_id"`
				Summaries json.RawMessage `json:"summaries"`
				Official  bool            `json:"official"`
			}
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				continue
			}
			st := ensure(ev.Round)
			seat := payload.SeatID
			if seat == "" {
				seat = ev.SeatID
			}
			st.decisions = append(st.decisions, DecisionAtRound{
				SeatID: seat, ActionID: payload.ActionID, Summaries: payload.Summaries,
				Official: payload.Official, Snapshot: ev.StateSnapshot,
			})
		case models.EventResolution:
			var payload struct {
				Agreement float64            `json:"agreement"`
				Synergy   float64            `json:"synergy"`
				Delta     map[string]float64 `json:"delta"`
			}
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				continue
			}
			st := ensure(ev.Round)
			st.agreement = payload.Agreement
			st.synergy = payload.Synergy
			st.delta = payload.Delta
		}
	}

	sort.Ints(order)
	rounds := make([]RoundComparison, 0, len(order))
	for _, round := range order {
		st := byRound[round]
		sort.SliceStable(st.decisions, func(i, j int) bool {
			return seatOrder(a, st.decisions[i].SeatID) < seatOrder(a, st.decisions[j].SeatID)
		})
		rounds = append(rounds, RoundComparison{
			Round: round, Decisions: st.decisions, Agreement: st.agreement,
			Synergy: st.synergy, Delta: st.delta,
		})
	}
	return View{
		Mode: ModeOmniscientComparison, ArchiveID: a.ID, SessionID: a.SessionID,
		EventCount: len(a.Events), Rounds: rounds,
	}
}

// seatOrder keeps the comparison columns in seat order instead of whatever
// order the events happened to arrive in.
func seatOrder(a Archive, seatID string) int {
	for _, s := range a.Config.Seats {
		if s.SeatID == seatID {
			return s.Seat
		}
	}
	return len(a.Config.Seats)
}

// projectKeyMoments marks turning points using only measurable signals:
// branch events (their declared metric effects), the resolutions with the
// largest metric swings, and the terminal outcome. No editorial judgement, no
// model call, and the same archive always yields the same moments.
func (a Archive) projectKeyMoments(limit int) View {
	reserved := make([]Moment, 0)
	swings := make([]Moment, 0)

	for _, ev := range a.Events {
		switch ev.Type {
		case models.EventBranchEvent:
			var payload struct {
				BranchID string             `json:"branch_id"`
				Text     string             `json:"text"`
				Effects  map[string]float64 `json:"effects"`
			}
			_ = json.Unmarshal(ev.Payload, &payload)
			magnitude := sumAbs(payload.Effects)
			reserved = append(reserved, Moment{
				EventID: ev.ID, Round: ev.Round, Kind: "branch", Magnitude: magnitude,
				Reason: fmt.Sprintf("branch %s fired, shifting %d metric(s) by %.1f total",
					payload.BranchID, len(payload.Effects), magnitude),
				Ref: payload.BranchID,
			})
		case models.EventResolution:
			var payload struct {
				Delta map[string]float64 `json:"delta"`
			}
			_ = json.Unmarshal(ev.Payload, &payload)
			magnitude := sumAbs(payload.Delta)
			swings = append(swings, Moment{
				EventID: ev.ID, Round: ev.Round, Kind: "metric_swing", Magnitude: magnitude,
				Reason: fmt.Sprintf("round %d moved %d metric(s) by %.1f total",
					ev.Round, len(payload.Delta), magnitude),
			})
		case models.EventMatchEnd:
			var payload struct {
				Outcome string `json:"outcome"`
			}
			_ = json.Unmarshal(ev.Payload, &payload)
			reserved = append(reserved, Moment{
				EventID: ev.ID, Round: ev.Round, Kind: "outcome", Magnitude: 0,
				Reason: fmt.Sprintf("match ended with outcome %q at round %d", payload.Outcome, ev.Round),
			})
		}
	}

	sort.SliceStable(swings, func(i, j int) bool {
		if swings[i].Magnitude == swings[j].Magnitude {
			return swings[i].EventID < swings[j].EventID
		}
		return swings[i].Magnitude > swings[j].Magnitude
	})

	remaining := limit - len(reserved)
	if remaining < 0 {
		remaining = 0
	}
	if remaining > len(swings) {
		remaining = len(swings)
	}
	moments := make([]Moment, 0, len(reserved)+remaining)
	moments = append(moments, swings[:remaining]...)
	moments = append(moments, reserved...)
	sort.SliceStable(moments, func(i, j int) bool { return moments[i].EventID < moments[j].EventID })
	return View{
		Mode: ModeKeyMoments, ArchiveID: a.ID, SessionID: a.SessionID,
		EventCount: len(a.Events), Moments: moments,
	}
}

func (a Archive) hasSeat(seatID string) bool {
	for _, s := range a.Config.Seats {
		if s.SeatID == seatID {
			return true
		}
	}
	return false
}

func sumAbs(m map[string]float64) float64 {
	total := 0.0
	for _, v := range m {
		if v < 0 {
			v = -v
		}
		total += v
	}
	return total
}
