package models

// MemoryKind is one of the six memory types from story 007 AC2.
type MemoryKind string

const (
	MemoryGameEvent          MemoryKind = "game_event"
	MemorySocialInteraction  MemoryKind = "social_interaction"
	MemoryPersonalReflection MemoryKind = "personal_reflection"
	MemoryRelationshipChange MemoryKind = "relationship_change"
	MemoryToolResult         MemoryKind = "tool_result"
	MemoryQuietObservation   MemoryKind = "quiet_observation"
)

// MemorySource makes every memory traceable to the event that produced it.
type MemorySource struct {
	Kind  string `json:"kind"`
	RefID string `json:"ref_id"`
	Round int    `json:"round"`
}

// Memory is stored locally only. It is never synced to the Hub or written into
// an EigenFlux Agent Card projection.
type Memory struct {
	ID          string       `json:"id"`
	CompanionID string       `json:"companion_id"`
	Kind        MemoryKind   `json:"kind"`
	Content     string       `json:"content"`
	Source      MemorySource `json:"source"`
	Strength    float64      `json:"strength"`
	CreatedAt   int64        `json:"created_at"`
}
