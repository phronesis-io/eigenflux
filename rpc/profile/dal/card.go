package dal

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

// ProfileChangeEvent is one append-only field-level profile change record.
// Unlike BioHistory (bio-only, legacy), this covers every editable Card field.
// JSON columns are stored as raw JSONB text; callers marshal/unmarshal.
type ProfileChangeEvent struct {
	ID             int64  `gorm:"column:id;primaryKey"`
	AgentID        int64  `gorm:"column:agent_id;not null"`
	SourceVersion  int64  `gorm:"column:source_version;not null"`
	ActorType      string `gorm:"column:actor_type;type:varchar(20);not null"`
	ActorID        string `gorm:"column:actor_id;type:varchar(100);not null;default:''"`
	Source         string `gorm:"column:source;type:varchar(100);not null;default:''"`
	Reason         string `gorm:"column:reason;type:text;not null;default:''"`
	ChangedPaths   string `gorm:"column:changed_paths;type:jsonb;not null;default:'[]'"`
	PreviousValues string `gorm:"column:previous_values;type:jsonb;not null;default:'{}'"`
	NewValues      string `gorm:"column:new_values;type:jsonb;not null;default:'{}'"`
	RequestID      string `gorm:"column:request_id;type:varchar(100);not null;default:''"`
	CreatedAt      int64  `gorm:"column:created_at;not null"`
}

func (ProfileChangeEvent) TableName() string { return "agent_profile_change_events" }

// clampRunes bounds client-influenced strings to their column width. The
// insert runs inside the caller's profile-write transaction, so an oversized
// value (e.g. an unbounded X-Bio-Source / X-Request-ID header) must degrade
// to truncation here, never to a varchar overflow that rolls the write back.
func clampRunes(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max])
}

// InsertProfileChangeEvent appends one change event. Meant to run inside the
// same transaction as the profile write it describes.
func InsertProfileChangeEvent(db *gorm.DB, ev *ProfileChangeEvent) error {
	if ev.CreatedAt == 0 {
		ev.CreatedAt = time.Now().UnixMilli()
	}
	ev.ActorType = clampRunes(ev.ActorType, 20)
	ev.ActorID = clampRunes(ev.ActorID, 100)
	ev.Source = clampRunes(ev.Source, 100)
	ev.RequestID = clampRunes(ev.RequestID, 100)
	if ev.ChangedPaths == "" {
		ev.ChangedPaths = "[]"
	}
	if ev.PreviousValues == "" {
		ev.PreviousValues = "{}"
	}
	if ev.NewValues == "" {
		ev.NewValues = "{}"
	}
	return db.Create(ev).Error
}

// ListRecentProfileChangeEvents returns the newest change events for an agent.
func ListRecentProfileChangeEvents(db *gorm.DB, agentID int64, limit int) ([]*ProfileChangeEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	var evs []*ProfileChangeEvent
	err := db.Where("agent_id = ?", agentID).Order("created_at DESC, id DESC").Limit(limit).Find(&evs).Error
	return evs, err
}

// EnsureAgentProfileRow makes sure an agent_profiles row exists so versioned
// updates have something to condition on. No-op when the row is present.
func EnsureAgentProfileRow(db *gorm.DB, agentID int64) error {
	return db.Exec(`INSERT INTO agent_profiles (agent_id, status, updated_at)
		VALUES (?, 0, ?) ON CONFLICT (agent_id) DO NOTHING`,
		agentID, time.Now().UnixMilli()).Error
}

// GetProfileVersionAndData reads the optimistic-lock version and the extended
// JSONB profile document. AgentProfile's struct intentionally does not map
// profile_data (so legacy Create/Update paths never touch it); access goes
// through this narrow reader instead.
func GetProfileVersionAndData(db *gorm.DB, agentID int64) (int64, map[string]json.RawMessage, error) {
	var row struct {
		ProfileVersion int64
		ProfileData    string
	}
	err := db.Table("agent_profiles").
		Select("profile_version, profile_data::text as profile_data").
		Where("agent_id = ?", agentID).
		Scan(&row).Error
	if err != nil {
		return 0, nil, err
	}
	data := map[string]json.RawMessage{}
	if row.ProfileData != "" {
		if uerr := json.Unmarshal([]byte(row.ProfileData), &data); uerr != nil {
			return row.ProfileVersion, map[string]json.RawMessage{}, nil
		}
	}
	return row.ProfileVersion, data, nil
}

// ApplyVersionedProfileDataUpdate merges dataMerge into profile_data and bumps
// profile_version, but only if the row still carries expectedVersion. Returns
// (newVersion, conflict=false) on success, (0, conflict=true) when someone
// else already bumped the version. dataMerge may be empty (version bump only,
// used when the write touched agents.* fields but no JSONB field).
func ApplyVersionedProfileDataUpdate(db *gorm.DB, agentID, expectedVersion int64, dataMerge map[string]json.RawMessage) (int64, bool, error) {
	mergeJSON := "{}"
	if len(dataMerge) > 0 {
		b, err := json.Marshal(dataMerge)
		if err != nil {
			return 0, false, err
		}
		mergeJSON = string(b)
	}
	var newVersion int64
	res := db.Raw(`UPDATE agent_profiles
		SET profile_version = profile_version + 1,
		    profile_data = profile_data || ?::jsonb,
		    updated_at = ?
		WHERE agent_id = ? AND profile_version = ?
		RETURNING profile_version`,
		mergeJSON, time.Now().UnixMilli(), agentID, expectedVersion).Scan(&newVersion)
	if res.Error != nil {
		return 0, false, res.Error
	}
	if res.RowsAffected == 0 {
		return 0, true, nil
	}
	return newVersion, false, nil
}

// BumpProfileVersion unconditionally increments profile_version (legacy write
// paths — old PUT /agents/profile — that carry no expected_version but must
// still invalidate concurrent versioned writers). Returns the new version.
func BumpProfileVersion(db *gorm.DB, agentID int64) (int64, error) {
	var newVersion int64
	res := db.Raw(`UPDATE agent_profiles
		SET profile_version = profile_version + 1, updated_at = ?
		WHERE agent_id = ?
		RETURNING profile_version`,
		time.Now().UnixMilli(), agentID).Scan(&newVersion)
	return newVersion, res.Error
}

// AgentCard is the rebuildable read projection row. Card JSON stays opaque
// text at the DAL layer; pkg/agentcard owns its shape.
type AgentCard struct {
	AgentID       int64  `gorm:"column:agent_id;primaryKey"`
	PublicCard    string `gorm:"column:public_card"`
	PrivateCard   string `gorm:"column:private_card"`
	SchemaVersion int32  `gorm:"column:schema_version"`
	SourceVersion int64  `gorm:"column:source_version"`
	CardVersion   int64  `gorm:"column:card_version"`
	GeneratedAt   int64  `gorm:"column:generated_at"`
}

func (AgentCard) TableName() string { return "agent_cards" }

// GetAgentCard loads one projection row (gorm.ErrRecordNotFound when absent).
func GetAgentCard(db *gorm.DB, agentID int64) (*AgentCard, error) {
	var card AgentCard
	err := db.Raw(`SELECT agent_id, public_card::text as public_card,
			private_card::text as private_card, schema_version,
			source_version, card_version, generated_at
		FROM agent_cards WHERE agent_id = ?`, agentID).Scan(&card).Error
	if err != nil {
		return nil, err
	}
	if card.AgentID == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &card, nil
}

// UpsertAgentCard writes a rebuilt projection. The WHERE guard drops stale
// rebuilds: an event carrying an older profile_version must not overwrite a
// projection already built from a newer one (equal versions are accepted —
// relation/influence-triggered rebuilds don't bump profile_version).
func UpsertAgentCard(db *gorm.DB, agentID int64, publicCard, privateCard string, schemaVersion int32, sourceVersion int64) error {
	return db.Exec(`INSERT INTO agent_cards
			(agent_id, public_card, private_card, schema_version, source_version, card_version, generated_at)
		VALUES (?, ?::jsonb, ?::jsonb, ?, ?, 1, ?)
		ON CONFLICT (agent_id) DO UPDATE SET
			public_card    = EXCLUDED.public_card,
			private_card   = EXCLUDED.private_card,
			schema_version = EXCLUDED.schema_version,
			source_version = EXCLUDED.source_version,
			card_version   = agent_cards.card_version + 1,
			generated_at   = EXCLUDED.generated_at
		WHERE agent_cards.source_version <= EXCLUDED.source_version`,
		agentID, publicCard, privateCard, schemaVersion, sourceVersion, time.Now().UnixMilli()).Error
}
