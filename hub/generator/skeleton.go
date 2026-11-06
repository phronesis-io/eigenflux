package generator

import (
	"fmt"
	"math"
	"math/rand"
	"strconv"

	"fluxspace.ai/hub/models"
)

// Tuning constants transcribed from TUNING in fluxspace/_check.js. Changing
// any of these changes the duration guarantee and must be re-calibrated.
const (
	MaxSelected          = 4
	RoundsMin            = 4
	RoundsMax            = 6
	TargetMinutesMin     = 10
	ThinkCharMSStandard  = 28
	AvgLogChars          = 565
	SectionGapMSStandard = 260
	DedupMaxAttempts     = 60
)

// EstimateMinutes mirrors estimateMinutes() in the frozen prototype. It is the
// enforcement point for the GDD's "10+ minute flow" hard constraint, not a
// marketing estimate.
func EstimateMinutes(roundCount, aiCount int) int {
	perAISec := float64(AvgLogChars*ThinkCharMSStandard)/1000 +
		4*float64(SectionGapMSStandard)/1000 + 0.42
	ai := math.Max(1, float64(aiCount))
	perRoundSec := 20 + ai*perAISec + 35 + 18
	return int(math.Max(TargetMinutesMin, math.Round((float64(roundCount)*perRoundSec+120)/60)))
}

// GenerateOptions mirrors the opts passed to generateGame() in the prototype.
type GenerateOptions struct {
	Mode                string
	Pace                string // standard | fast
	CompanionCount      int
	HistoryFingerprints []string
	RecentThemes        []string
}

type theme struct {
	ID    string
	Title string
	Scene string
}

var deductionThemes = []theme{
	{"d1", "The Archived Testimony", "The archive wing lost power at 23:40. One page of the ledger is missing, and everyone on duty has a different account of who signed it out."},
	{"d2", "Signal from the Empty Platform", "A train that was cancelled keeps transmitting arrival telemetry. The station log and the signal disagree by eleven minutes."},
	{"d3", "The Glasshouse Inventory", "Four greenhouse wings report the same specimen count, yet the central catalogue shows one extra entry every night."},
	{"d4", "Rain over the Relay Station", "Rain washed out the outdoor traces, but the indoor relay preserved a fragment of a conversation nobody admits to having."},
	{"d5", "The Unlisted Timetable", "A timetable with no issuing authority circulates among the staff. Three of its stops do not exist on any map."},
}

var deductionClues = []models.Clue{
	{Key: "c_ledger", Label: "Sign-out ledger", Text: "The final signature uses a pen that was issued the next morning.", Gain: 12},
	{Key: "c_telemetry", Label: "Arrival telemetry", Text: "The eleven-minute offset repeats every cycle, so it is not clock drift.", Gain: 10},
	{Key: "c_catalogue", Label: "Catalogue diff", Text: "The extra entry always appears after the night shift handover.", Gain: 11},
	{Key: "c_relay", Label: "Relay fragment", Text: "A second voice is audible under the static, naming a gate that was sealed.", Gain: 9},
	{Key: "c_timetable", Label: "Timetable stock", Text: "The paper stock matches internal drafting supplies, not public print.", Gain: 13},
	{Key: "c_witness", Label: "Witness note", Text: "One witness describes the corridor lighting incorrectly for that hour.", Gain: 8},
}

var roundTemplates = []struct{ Title, Objective string }{
	{"Opening: what is missing", "Establish which records can actually be trusted"},
	{"First contradiction", "Find the pair of claims that cannot both hold"},
	{"The quiet witness", "Recover the account nobody volunteered"},
	{"Cross-check", "Tie physical evidence to the timeline"},
	{"Final reconstruction", "Commit to a single account of events"},
	{"Aftermath", "Record what remains unresolved"},
}

func deductionActionPool() []models.Action {
	return []models.Action{
		{ID: "a_testimony", Label: "Cross-check testimonies", Family: "verify", Risk: "low", InfoGain: 2, TeamHarm: 1, Contest: 0,
			Effects: map[string]float64{"truth": 8, "suspicion": 2}},
		{ID: "a_evidence", Label: "Trace the evidence chain", Family: "evidence", Risk: "mid", InfoGain: 3, TeamHarm: 1, Contest: 0,
			Effects: map[string]float64{"truth": 14, "suspicion": 5}},
		{ID: "a_probe", Label: "Probe with a false conclusion", Family: "probe", Risk: "high", InfoGain: 2, TeamHarm: -1, Contest: 1,
			Effects: map[string]float64{"truth": 10, "suspicion": 12}},
		{ID: "a_declare", Label: "Declare the current theory", Family: "declare", Risk: "mid", InfoGain: 1, TeamHarm: 2, Contest: 0,
			Effects: map[string]float64{"truth": 6, "suspicion": -6}},
		{ID: "a_wait", Label: "Observe in silence", Family: "wait", Risk: "low", InfoGain: 0, TeamHarm: -1, Contest: 0,
			Effects: map[string]float64{"truth": 2, "suspicion": -8}},
	}
}

func deductionBranches() []models.Branch {
	return []models.Branch{
		{ID: "b_wipe", Weight: 3, When: "resolve", Text: "A witness retracts a statement; progress stalls but suspicion eases.",
			Effects: map[string]float64{"truth": -6, "suspicion": -8}},
		{ID: "b_new", Weight: 2, When: "resolve", Text: "An unregistered timetable surfaces.",
			Effects: map[string]float64{"truth": 12, "suspicion": 3}},
		{ID: "b_leak", Weight: 2, When: "resolve", Text: "The investigation leaks; records are removed before they can be read.",
			Effects: map[string]float64{"truth": -4, "suspicion": 9}},
		{ID: "b_rain", Weight: 2, When: "observe", Text: "Rain erases outdoor traces; indoor evidence sharpens.",
			Effects: map[string]float64{"truth": 4, "suspicion": 0}},
		{ID: "b_pressure", Weight: 2, When: "resolve", Condition: &models.Condition{Metric: "suspicion", Op: ">=", Value: 55},
			Text:    "Management demands a conclusion by the next round.",
			Effects: map[string]float64{"truth": -3, "suspicion": 6}},
		{ID: "b_ally", Weight: 2, When: "resolve", Condition: &models.Condition{Metric: "truth", Op: ">=", Value: 55},
			Text:    "An insider agrees with the theory and shares an old file.",
			Effects: map[string]float64{"truth": 9, "suspicion": -4}},
	}
}

// pickActions mirrors pickActions() in the prototype: one action from each
// risk tier, then fill to four from the remainder, then shuffle.
func pickActions(pool []models.Action, rng *rand.Rand) []models.Action {
	byRisk := map[string][]models.Action{"low": {}, "mid": {}, "high": {}}
	for _, a := range pool {
		byRisk[a.Risk] = append(byRisk[a.Risk], a)
	}
	out := make([]models.Action, 0, 4)
	for _, risk := range []string{"low", "mid", "high"} {
		bucket := byRisk[risk]
		if len(bucket) > 0 {
			out = append(out, bucket[rng.Intn(len(bucket))])
		}
	}
	chosen := make(map[string]bool, len(out))
	for _, a := range out {
		chosen[a.ID] = true
	}
	rest := make([]models.Action, 0, len(pool))
	for _, a := range pool {
		if !chosen[a.ID] {
			rest = append(rest, a)
		}
	}
	rng.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
	for len(out) < 4 && len(rest) > 0 {
		out = append(out, rest[len(rest)-1])
		rest = rest[:len(rest)-1]
	}
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

func buildDeduction(roundCount int, rng *rand.Rand) *models.Skeleton {
	t := deductionThemes[rng.Intn(len(deductionThemes))]
	pool := deductionActionPool()
	clues := append([]models.Clue(nil), deductionClues...)
	rng.Shuffle(len(clues), func(i, j int) { clues[i], clues[j] = clues[j], clues[i] })

	rounds := make([]models.Round, 0, roundCount)
	tensions := []int{2, 3, 4, 5, 6, 6}
	for i := 0; i < roundCount; i++ {
		tpl := roundTemplates[i%len(roundTemplates)]
		tension := tensions[len(tensions)-1]
		if i < len(tensions) {
			tension = tensions[i]
		}
		rounds = append(rounds, models.Round{
			Index:     i + 1,
			Title:     tpl.Title,
			Objective: tpl.Objective,
			Reveal:    []models.Clue{clues[i%len(clues)]},
			Actions:   pickActions(pool, rng),
			Tension:   tension,
		})
	}

	return &models.Skeleton{
		Type:      "deduction",
		ThemeID:   t.ID,
		Title:     t.Title,
		ThemeText: t.Scene,
		Rules: []string{
			"This match runs " + strconv.Itoa(roundCount) + " rounds; one new clue unlocks per round.",
			"Every participant picks one line of inquiry; all actions settle in parallel.",
			"Truth proximity >= 80 with suspicion < 70 closes the case.",
			"Suspicion >= 92 forces an early close and counts as a loss.",
			"Remaining time ticks down every round; reaching zero is a loss.",
		},
		WinCondition: models.Condition{All: []models.Condition{
			{Metric: "truth", Op: ">=", Value: 80},
			{Metric: "suspicion", Op: "<", Value: 70},
		}},
		LoseCondition: models.Condition{Any: []models.Condition{
			{Metric: "suspicion", Op: ">=", Value: 92},
			{Metric: "time", Op: "<=", Value: 0},
		}},
		InitialMetrics: []models.Metric{
			{Key: "truth", Label: "Truth proximity", Value: 12, Max: 100, Good: "high"},
			{Key: "suspicion", Label: "Suspicion", Value: 22, Max: 100, Good: "low"},
			{Key: "time", Label: "Time remaining", Value: 100, Max: 100, Good: "high"},
		},
		Resources: []models.Metric{{Key: "influence", Label: "Influence", Value: 3, Max: 8}},
		Rounds:    rounds,
		Branches:  deductionBranches(),
		RoundTick: map[string]float64{"time": -18},
	}
}

// GenerateSkeleton mirrors generateGame()'s dedup loop: up to 60 attempts,
// strict theme avoidance for the first two thirds, fingerprint uniqueness
// throughout, and a clearly flagged degraded fallback.
func GenerateSkeleton(opts GenerateOptions, rng *rand.Rand) (*models.Skeleton, error) {
	if opts.CompanionCount < 1 || opts.CompanionCount > MaxSelected {
		return nil, fmt.Errorf("companion count %d outside [1,%d]", opts.CompanionCount, MaxSelected)
	}
	roundCount := 5
	if opts.Pace == "fast" {
		roundCount = 3
	} else if opts.Mode == "solo" {
		roundCount = 6
	}

	used := make(map[string]bool, len(opts.HistoryFingerprints))
	for _, f := range opts.HistoryFingerprints {
		used[f] = true
	}
	banned := make(map[string]bool, len(opts.RecentThemes))
	for _, t := range opts.RecentThemes {
		banned[t] = true
	}

	attempts := DedupMaxAttempts
	// The prototype compares against attempts*0.66 (39.6 for 60), so the
	// equivalent integer boundary is ceil(...) = 40, i.e. attempts 0..39.
	strictLimit := int(math.Ceil(float64(attempts) * 0.66))

	var result *models.Skeleton
	for attempt := 0; attempt < attempts; attempt++ {
		candidate := buildDeduction(roundCount, rng)
		strictTheme := attempt < strictLimit
		if strictTheme && banned[candidate.ThemeID] {
			continue
		}
		if used[Fingerprint(candidate)] {
			continue
		}
		result = candidate
		break
	}
	if result == nil {
		result = buildDeduction(roundCount, rng)
		result.DedupDegraded = true
	}

	result.Fingerprint = Fingerprint(result)
	result.StructuralHash = StructuralHash(result)
	result.ContentHash = ContentHash(result)
	result.EstimatedMinutes = EstimateMinutes(roundCount, opts.CompanionCount)
	return result, nil
}
