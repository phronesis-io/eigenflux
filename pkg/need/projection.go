package need

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
)

var ErrProjectionConflict = errors.New("normalized_need_revision_conflict")

type Projection struct {
	NormalizedNeedID  int64           `gorm:"primaryKey" json:"normalized_need_id,string"`
	NeedInputID       int64           `json:"-"`
	AgentID           int64           `json:"-"`
	IntentID          int64           `json:"-"`
	IntentVersion     int64           `json:"-"`
	SchemaVersion     string          `json:"schema_version"`
	Normalized        json.RawMessage `gorm:"type:jsonb" json:"normalized"`
	NormalizerVersion string          `json:"normalizer_version"`
	TaxonomyVersion   string          `json:"taxonomy_version"`
	MappingStatus     string          `json:"mapping_status"`
	Status            string          `json:"status"`
	CreatedAt         int64           `json:"created_at"`
	UpdatedAt         int64           `json:"updated_at"`
	Eligible          bool            `gorm:"->" json:"eligible"`
}

func (Projection) TableName() string { return "normalized_needs" }

func (s Store) insertProjection(tx *gorm.DB, input row, n Normalized, normalizer, taxonomy string, now int64) (Projection, error) {
	raw, err := json.Marshal(n)
	if err != nil {
		return Projection{}, err
	}
	id, err := s.IDs.NextID()
	if err != nil {
		return Projection{}, err
	}
	p := Projection{
		NormalizedNeedID: id, NeedInputID: input.NeedInputID, AgentID: input.AgentID,
		IntentID: input.IntentID, IntentVersion: input.IntentVersion,
		SchemaVersion: NormalizedSchemaVersion, Normalized: raw, NormalizerVersion: normalizer,
		TaxonomyVersion: taxonomy, MappingStatus: n.MappingStatus(), Status: "active",
		CreatedAt: now, UpdatedAt: now,
	}
	err = tx.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}).Create(&p).Error
	return p, err
}

// attachProjections reads MVCC snapshots: a slow or failing offline publisher
// cannot make readers wait on its row lock or hide the last committed projection.
func (s Store) attachProjections(ctx context.Context, records []Record) error {
	if len(records) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(records))
	for _, r := range records {
		ids = append(ids, r.NeedInputID)
	}
	var projections []Projection
	err := s.DB.WithContext(ctx).Raw(`SELECT n.*, EXISTS (
		SELECT 1 FROM current_normalized_needs c WHERE c.normalized_need_id = n.normalized_need_id
	) AS eligible FROM normalized_needs n
	WHERE n.agent_id = ? AND n.need_input_id IN ? AND n.status = 'active'`, records[0].AgentID, ids).Scan(&projections).Error
	if err != nil {
		return err
	}
	byID := make(map[int64]*Projection, len(projections))
	for i := range projections {
		byID[projections[i].NeedInputID] = &projections[i]
	}
	for i := range records {
		records[i].NormalizedNeed = byID[records[i].NeedInputID]
	}
	return nil
}

// Enrich is an internal publication boundary, not an online request dependency.
// The caller supplies an offline-built snapshot and the active projection ID it
// observed. All computation happens before the short atomic replacement. A stale
// publisher, bad vocabulary, or storage failure leaves the active result intact.
func (s Store) Enrich(ctx context.Context, owner, inputID, expectedProjectionID int64, vocabulary Vocabulary, now int64) (Projection, error) {
	r, err := s.Get(ctx, owner, inputID)
	if err != nil {
		return Projection{}, err
	}
	if r.NormalizedNeed == nil || !r.NormalizedNeed.Eligible {
		return Projection{}, ErrStaleIntent
	}
	in, err := Decode(r.Input)
	if err != nil {
		return Projection{}, err
	}
	n, err := NormalizeWithVocabulary(in, vocabulary)
	if err != nil {
		return Projection{}, err
	}
	encoded, err := json.Marshal(n)
	if err != nil {
		return Projection{}, err
	}
	_, expectedHash, err := canonical(encoded)
	if err != nil {
		return Projection{}, err
	}
	var result Projection
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing Projection
		err := tx.Where("agent_id = ? AND need_input_id = ? AND normalizer_version = ? AND taxonomy_version = ?", owner, inputID, VocabularyNormalizerVersion, vocabulary.Version).First(&existing).Error
		if err == nil {
			_, savedHash, err := canonical(existing.Normalized)
			if err != nil {
				return err
			}
			if savedHash != expectedHash || existing.Status != "active" {
				return ErrProjectionConflict
			}
			result = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		updated := tx.Model(&Projection{}).Where("agent_id = ? AND need_input_id = ? AND normalized_need_id = ? AND status = 'active'", owner, inputID, expectedProjectionID).Updates(map[string]any{"status": "superseded", "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrProjectionConflict
		}
		result, err = s.insertProjection(tx, row{NeedInputID: r.NeedInputID, AgentID: owner, IntentID: r.IntentID, IntentVersion: r.IntentVersion}, n, VocabularyNormalizerVersion, vocabulary.Version, now)
		return err
	})
	if err != nil {
		return Projection{}, err
	}
	// Re-read eligibility after publication; an Intent edit may have raced it.
	var eligible bool
	err = s.DB.WithContext(ctx).Raw("SELECT EXISTS (SELECT 1 FROM current_normalized_needs WHERE normalized_need_id = ?)", result.NormalizedNeedID).Scan(&eligible).Error
	result.Eligible = eligible
	return result, err
}
