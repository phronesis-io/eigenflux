// Package models holds the domain and wire types shared by the FluxSpace Hub.
// Field semantics are transcribed from design/game-brief.md and the frozen
// prototype fluxspace/_check.js. Do not invent aliases for these names.
package models

// Metric is one progress or danger gauge in a match.
type Metric struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Max   float64 `json:"max"`
	Good  string  `json:"good"` // "high" or "low"
}

// Action is one candidate decision from a skeleton's action pool.
type Action struct {
	ID       string             `json:"id"`
	Label    string             `json:"label"`
	Family   string             `json:"family"`
	Risk     string             `json:"risk"` // low | mid | high
	InfoGain float64            `json:"info_gain"`
	TeamHarm float64            `json:"team_harm"`
	Contest  float64            `json:"contest"`
	Effects  map[string]float64 `json:"effects"`
}

// Condition is a recursive win/lose predicate. Value may be a number or one
// of the sentinels "__my__" / "__their__" resolved against the live metrics.
type Condition struct {
	All    []Condition `json:"all,omitempty"`
	Any    []Condition `json:"any,omitempty"`
	Metric string      `json:"metric,omitempty"`
	Op     string      `json:"op,omitempty"`
	Value  any         `json:"value,omitempty"`
}

// Clue is a piece of information revealed in a round.
type Clue struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Text  string  `json:"text"`
	Gain  float64 `json:"gain"`
}

// Round is one act of the theatre.
type Round struct {
	Index     int      `json:"index"`
	Title     string   `json:"title"`
	Objective string   `json:"objective"`
	Reveal    []Clue   `json:"reveal"`
	Actions   []Action `json:"actions"`
	Tension   int      `json:"tension"`
}

// Branch is a conditional mid-match event with metric effects.
type Branch struct {
	ID        string             `json:"id"`
	Weight    int                `json:"weight"`
	When      string             `json:"when"` // observe | resolve
	Condition *Condition         `json:"condition,omitempty"`
	Text      string             `json:"text"`
	Effects   map[string]float64 `json:"effects"`
}

// Skeleton is the topology layer of a generated game (GDD three-layer schema:
// skeleton -> narrative -> session instance).
type Skeleton struct {
	Type             string             `json:"type"`
	ThemeID          string             `json:"theme_id"`
	Title            string             `json:"title"`
	ThemeText        string             `json:"theme_text"`
	Rules            []string           `json:"rules"`
	WinCondition     Condition          `json:"win_condition"`
	LoseCondition    Condition          `json:"lose_condition"`
	InitialMetrics   []Metric           `json:"initial_metrics"`
	Resources        []Metric           `json:"resources"`
	Rounds           []Round            `json:"rounds"`
	Branches         []Branch           `json:"branches"`
	RoundTick        map[string]float64 `json:"round_tick"`
	Fingerprint      string             `json:"fingerprint"`
	StructuralHash   string             `json:"structural_hash"`
	ContentHash      string             `json:"content_hash"`
	EstimatedMinutes int                `json:"estimated_minutes"`
	DedupDegraded    bool               `json:"dedup_degraded"`
}

// Personality is the six-dimension companion vector (0-100). These are real
// inputs to the decision engine, not display copy.
type Personality struct {
	Risk       float64 `json:"risk"`
	Logic      float64 `json:"logic"`
	Empathy    float64 `json:"empathy"`
	Coop       float64 `json:"coop"`
	Aggression float64 `json:"aggression"`
	Intuition  float64 `json:"intuition"`
}

// Decision is the validated output of a decision engine call.
type Decision struct {
	ActionID         string  `json:"action_id"`
	ReasoningSummary string  `json:"reasoning_summary"`
	Confidence       float64 `json:"confidence"`
	Source           string  `json:"source"` // rule_engine | model | fallback
}
