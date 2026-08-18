package dal

import (
	"encoding/json"
	"fmt"
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

// LatestProfileFieldChange is the newest audit metadata for one editable path.
// Querying per path avoids the arbitrary "latest N events" cutoff that could
// hide an old-but-still-current human edit after other fields churned.
type LatestProfileFieldChange struct {
	Path          string `gorm:"column:path"`
	PreviousValue string `gorm:"column:previous_value"`
	UpdatedAt     int64  `gorm:"column:updated_at"`
	ActorType     string `gorm:"column:actor_type"`
}

// ListLatestProfileFieldChanges returns exactly the newest event per changed
// path for one agent. One multi-path event may therefore produce several rows.
func ListLatestProfileFieldChanges(db *gorm.DB, agentID int64) ([]*LatestProfileFieldChange, error) {
	var rows []*LatestProfileFieldChange
	err := db.Raw(`SELECT DISTINCT ON (changed.path)
			changed.path AS path,
			COALESCE((ev.previous_values -> changed.path)::text, 'null') AS previous_value,
			ev.created_at AS updated_at,
			ev.actor_type
		FROM agent_profile_change_events AS ev
		CROSS JOIN LATERAL jsonb_array_elements_text(ev.changed_paths) AS changed(path)
		WHERE ev.agent_id = ?
		ORDER BY changed.path, ev.created_at DESC, ev.id DESC`, agentID).Scan(&rows).Error
	return rows, err
}

// TrimSupersededProfileChangeEventPathsBefore removes only the obsolete paths
// from old multi-field events. Without this pass, one field that never changes
// again would keep unrelated superseded values (including PII) in the same row
// forever. The newest event for every agent/path is always retained.
func TrimSupersededProfileChangeEventPathsBefore(db *gorm.DB, beforeCreatedAtMs int64, batchSize, maxBatches int) (int64, bool, error) {
	if batchSize <= 0 {
		batchSize = 5000
	}
	if maxBatches <= 0 {
		maxBatches = 1
	}
	var total int64
	for batch := 0; batch < maxBatches; batch++ {
		res := db.Exec(`WITH candidates AS (
			SELECT old.id,
			       array_agg(DISTINCT changed.path) AS remove_paths
			FROM agent_profile_change_events AS old
			CROSS JOIN LATERAL jsonb_array_elements_text(old.changed_paths) AS changed(path)
			WHERE old.created_at < ?
			  AND EXISTS (
				SELECT 1
				FROM agent_profile_change_events AS newer
				WHERE newer.agent_id = old.agent_id
				  AND (newer.created_at > old.created_at
				       OR (newer.created_at = old.created_at AND newer.id > old.id))
				  AND jsonb_exists(newer.changed_paths, changed.path)
			  )
			GROUP BY old.id, old.created_at
			ORDER BY old.created_at, old.id
			LIMIT ?
		)
		UPDATE agent_profile_change_events AS old
		SET changed_paths = old.changed_paths - candidates.remove_paths,
		    previous_values = old.previous_values - candidates.remove_paths,
		    new_values = old.new_values - candidates.remove_paths
		FROM candidates
		WHERE old.id = candidates.id`, beforeCreatedAtMs, batchSize)
		if res.Error != nil {
			return total, false, res.Error
		}
		total += res.RowsAffected
		if res.RowsAffected < int64(batchSize) {
			return total, false, nil
		}
	}
	return total, true, nil
}

// DeleteSupersededProfileChangeEventsBefore removes old audit rows only when
// every path in that row has a newer event for the same agent. This bounds the
// append-only history while preserving the latest human/agent attribution,
// previous value, and timestamp for every field indefinitely — refresh-context
// relies on that latest event to protect human edits.
func DeleteSupersededProfileChangeEventsBefore(db *gorm.DB, beforeCreatedAtMs int64, batchSize, maxBatches int) (int64, bool, error) {
	if batchSize <= 0 {
		batchSize = 5000
	}
	if maxBatches <= 0 {
		maxBatches = 1
	}
	var total int64
	for batch := 0; batch < maxBatches; batch++ {
		res := db.Exec(`WITH candidates AS (
			SELECT old.id
			FROM agent_profile_change_events AS old
			WHERE old.created_at < ?
			  AND NOT EXISTS (
				SELECT 1
				FROM jsonb_array_elements_text(old.changed_paths) AS changed(path)
				WHERE NOT EXISTS (
					SELECT 1
					FROM agent_profile_change_events AS newer
					WHERE newer.agent_id = old.agent_id
					  AND (newer.created_at > old.created_at
					       OR (newer.created_at = old.created_at AND newer.id > old.id))
					  AND jsonb_exists(newer.changed_paths, changed.path)
				)
			  )
			ORDER BY old.created_at, old.id
			LIMIT ?
		)
		DELETE FROM agent_profile_change_events AS doomed
		USING candidates
		WHERE doomed.id = candidates.id`, beforeCreatedAtMs, batchSize)
		if res.Error != nil {
			return total, false, res.Error
		}
		total += res.RowsAffected
		if res.RowsAffected < int64(batchSize) {
			return total, false, nil
		}
	}
	return total, true, nil
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
	version, data, _, err := GetProfileVersionDataAndUpdatedAt(db, agentID)
	return version, data, err
}

// GetProfileVersionDataAndUpdatedAt additionally returns the fact row's
// updated_at for Agent Card's data-update timestamp. It remains one SQL read so
// version, document, and timestamp describe the same committed row.
func GetProfileVersionDataAndUpdatedAt(db *gorm.DB, agentID int64) (int64, map[string]json.RawMessage, int64, error) {
	var row struct {
		ProfileVersion int64
		ProfileData    string
		UpdatedAt      int64
	}
	err := db.Table("agent_profiles").
		Select("profile_version, profile_data::text as profile_data, updated_at").
		Where("agent_id = ?", agentID).
		Scan(&row).Error
	if err != nil {
		return 0, nil, 0, err
	}
	data := map[string]json.RawMessage{}
	if row.ProfileData != "" {
		if uerr := json.Unmarshal([]byte(row.ProfileData), &data); uerr != nil {
			return row.ProfileVersion, map[string]json.RawMessage{}, row.UpdatedAt, nil
		}
	}
	return row.ProfileVersion, data, row.UpdatedAt, nil
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
	AgentID               int64  `gorm:"column:agent_id;primaryKey"`
	PublicCard            string `gorm:"column:public_card"`
	PrivateCard           string `gorm:"column:private_card"`
	SchemaVersion         int32  `gorm:"column:schema_version"`
	SourceVersion         int64  `gorm:"column:source_version"`
	RebuildFence          int64  `gorm:"column:rebuild_fence"`
	CardVersion           int64  `gorm:"column:card_version"`
	PublicCardVersion     int64  `gorm:"column:public_card_version"`
	GeneratedAt           int64  `gorm:"column:generated_at"`
	PublicCardGeneratedAt int64  `gorm:"column:public_card_generated_at"`
}

func (AgentCard) TableName() string { return "agent_cards" }

// GetAgentCard loads one projection row (gorm.ErrRecordNotFound when absent).
func GetAgentCard(db *gorm.DB, agentID int64) (*AgentCard, error) {
	var card AgentCard
	err := db.Raw(`SELECT agent_id, public_card::text as public_card,
			private_card::text as private_card, schema_version,
			source_version, rebuild_fence, card_version, public_card_version,
			generated_at, public_card_generated_at
		FROM agent_cards WHERE agent_id = ?`, agentID).Scan(&card).Error
	if err != nil {
		return nil, err
	}
	if card.AgentID == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &card, nil
}

// GetAgentCards loads the already-built public projection for a bounded set of
// agents in one query. Missing cards are intentionally absent from the result:
// list endpoints must not synchronously rebuild one card per peer.
func GetAgentCards(db *gorm.DB, agentIDs []int64) (map[int64]*AgentCard, error) {
	cardsByAgent := make(map[int64]*AgentCard)
	if len(agentIDs) == 0 {
		return cardsByAgent, nil
	}

	deduplicated := make([]int64, 0, len(agentIDs))
	seen := make(map[int64]struct{}, len(agentIDs))
	for _, agentID := range agentIDs {
		if agentID <= 0 {
			continue
		}
		if _, ok := seen[agentID]; ok {
			continue
		}
		seen[agentID] = struct{}{}
		deduplicated = append(deduplicated, agentID)
	}
	if len(deduplicated) == 0 {
		return cardsByAgent, nil
	}

	var cards []*AgentCard
	err := db.Raw(`SELECT agent_id, public_card::text as public_card,
			schema_version, public_card_version, public_card_generated_at
		FROM agent_cards
		WHERE agent_id IN ?`, deduplicated).Scan(&cards).Error
	if err != nil {
		return nil, err
	}
	for _, card := range cards {
		cardsByAgent[card.AgentID] = card
	}
	return cardsByAgent, nil
}

// NextAgentCardRebuildFence allocates a database-monotonic fencing token for
// one rebuild attempt. It remains correct when a Redis lease holder resumes
// after expiry: a newer holder has a larger fence and wins the upsert.
func NextAgentCardRebuildFence(db *gorm.DB) (int64, error) {
	var fence int64
	result := db.Raw(`SELECT nextval('agent_card_rebuild_fence_seq')`).Scan(&fence)
	return fence, result.Error
}

// UpsertAgentCard writes a rebuilt projection. The WHERE guard drops stale
// rebuilds and skips equal projections. A newer source_version is still
// recorded even when the JSON is unchanged, so a later stale rebuild cannot
// overwrite the row; card_version/generated_at advance only when visible card
// content changes.
// Deprecated: this legacy signature cannot be made safe because the caller has
// already read facts before entering the function. It deliberately fails
// closed. In-repository callers use UpsertAgentCardWithFence; the symbol stays
// temporarily so mixed source trees fail explicitly instead of silently
// compiling an unfenced writer.
func UpsertAgentCard(db *gorm.DB, agentID int64, publicCard, privateCard string, schemaVersion int32, sourceVersion int64) error {
	return fmt.Errorf("UpsertAgentCard is disabled: allocate a fence before fact reads and call UpsertAgentCardWithFence")
}

// UpsertAgentCardWithFence persists a projection only when this rebuild is
// newer than the last accepted rebuild and its source version is not older.
// The fence is advanced even for an identical card: otherwise a stale holder
// could write a different equal-version snapshot after a newer no-op rebuild.
func UpsertAgentCardWithFence(db *gorm.DB, agentID int64, publicCard, privateCard string, schemaVersion int32, sourceVersion, rebuildFence int64) error {
	now := time.Now().UnixMilli()
	result := db.Exec(`INSERT INTO agent_cards
			(agent_id, public_card, private_card, schema_version, source_version, rebuild_fence,
			 card_version, public_card_version, generated_at, public_card_generated_at)
		VALUES (?, ?::jsonb, ?::jsonb, ?, ?, ?, 1, 1, ?, ?)
		ON CONFLICT (agent_id) DO UPDATE SET
			public_card    = EXCLUDED.public_card,
			private_card   = EXCLUDED.private_card,
			schema_version = EXCLUDED.schema_version,
			source_version = EXCLUDED.source_version,
			rebuild_fence  = EXCLUDED.rebuild_fence,
			card_version   = agent_cards.card_version + CASE WHEN
				agent_cards.public_card IS DISTINCT FROM EXCLUDED.public_card
				OR agent_cards.private_card IS DISTINCT FROM EXCLUDED.private_card
				OR agent_cards.schema_version IS DISTINCT FROM EXCLUDED.schema_version
				THEN 1 ELSE 0 END,
			public_card_version = agent_cards.public_card_version + CASE WHEN
				agent_cards.public_card IS DISTINCT FROM EXCLUDED.public_card
				OR agent_cards.schema_version IS DISTINCT FROM EXCLUDED.schema_version
				THEN 1 ELSE 0 END,
			generated_at   = CASE WHEN
				agent_cards.public_card IS DISTINCT FROM EXCLUDED.public_card
				OR agent_cards.private_card IS DISTINCT FROM EXCLUDED.private_card
				OR agent_cards.schema_version IS DISTINCT FROM EXCLUDED.schema_version
				THEN EXCLUDED.generated_at ELSE agent_cards.generated_at END,
			public_card_generated_at = CASE WHEN
				agent_cards.public_card IS DISTINCT FROM EXCLUDED.public_card
				OR agent_cards.schema_version IS DISTINCT FROM EXCLUDED.schema_version
				THEN EXCLUDED.public_card_generated_at ELSE agent_cards.public_card_generated_at END
		WHERE agent_cards.source_version < EXCLUDED.source_version
			OR (agent_cards.source_version = EXCLUDED.source_version
				AND agent_cards.rebuild_fence < EXCLUDED.rebuild_fence)`,
		agentID, publicCard, privateCard, schemaVersion, sourceVersion, rebuildFence, now, now)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	// A no-op is successful only when the stored projection already equals the
	// requested one. A newer but different row means this rebuild was rejected;
	// callers must not acknowledge its Redis snapshot as projected.
	var matches bool
	err := db.Raw(`SELECT EXISTS (
		SELECT 1 FROM agent_cards
		WHERE agent_id = ?
		  AND (source_version > ? OR (source_version = ? AND rebuild_fence >= ?))
		  AND public_card = ?::jsonb AND private_card = ?::jsonb
		  AND schema_version = ?
	)`, agentID, sourceVersion, sourceVersion, rebuildFence, publicCard, privateCard, schemaVersion).Scan(&matches).Error
	if err != nil {
		return err
	}
	if !matches {
		return fmt.Errorf("agent card projection was superseded for agent %d", agentID)
	}
	return nil
}
