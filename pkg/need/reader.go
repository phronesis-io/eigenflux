package need

import (
	"context"
	"encoding/json"
	"fmt"
)

// Snapshot is one immutable input and its currently eligible normalization.
// InputID is the stable public Need reference; ProjectionID identifies enrichment.
type Snapshot struct {
	InputID           int64      `json:"need_input_id,string"`
	ProjectionID      int64      `json:"normalized_need_id,string"`
	IntentID          int64      `json:"intent_id,string"`
	IntentVersion     int64      `json:"intent_version"`
	Input             Input      `json:"input"`
	Normalized        Normalized `json:"normalized"`
	NormalizerVersion string     `json:"normalizer_version"`
	TaxonomyVersion   string     `json:"taxonomy_version,omitempty"`
	MappingStatus     string     `json:"mapping_status"`
}

type snapshotRow struct {
	InputID, ProjectionID, IntentID, IntentVersion    int64
	Input, Normalized                                 json.RawMessage
	NormalizerVersion, TaxonomyVersion, MappingStatus string
}

const snapshotColumns = `i.need_input_id AS input_id, i.intent_id, i.intent_version, i.input,
 n.normalized_need_id AS projection_id, n.normalized, n.normalizer_version, n.taxonomy_version, n.mapping_status`

func (r snapshotRow) snapshot() (Snapshot, error) {
	in, err := Decode(r.Input)
	if err != nil {
		return Snapshot{}, fmt.Errorf("stored NeedInput: %w", err)
	}
	var normalized Normalized
	if err = json.Unmarshal(r.Normalized, &normalized); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{InputID: r.InputID, ProjectionID: r.ProjectionID, IntentID: r.IntentID,
		IntentVersion: r.IntentVersion, Input: in, Normalized: normalized, NormalizerVersion: r.NormalizerVersion,
		TaxonomyVersion: r.TaxonomyVersion, MappingStatus: r.MappingStatus}, nil
}

// Current reads ownership and eligibility in one MVCC statement. It never
// accepts a superseded projection or an input linked to an old Intent version.
func (s Store) Current(ctx context.Context, owner, inputID int64) (Snapshot, error) {
	var row snapshotRow
	err := s.DB.WithContext(ctx).Table("need_inputs i").Select(snapshotColumns).
		Joins("LEFT JOIN current_normalized_needs n ON n.need_input_id = i.need_input_id").
		Where("i.agent_id = ? AND i.need_input_id = ?", owner, inputID).Scan(&row).Error
	if err != nil {
		return Snapshot{}, err
	}
	if row.InputID == 0 {
		return Snapshot{}, ErrNotFound
	}
	if row.ProjectionID == 0 {
		return Snapshot{}, ErrStaleIntent
	}
	return row.snapshot()
}

// Active selects at most five matching Needs before any embedding/recall work.
// Unmapped vocabulary remains eligible; only source lifecycle and deadline gate it.
func (s Store) Active(ctx context.Context, owner int64, kinds []string, now int64) ([]Snapshot, error) {
	var rows []snapshotRow
	err := s.DB.WithContext(ctx).Table("need_inputs i").Select(snapshotColumns).
		Joins("JOIN current_normalized_needs n ON n.need_input_id = i.need_input_id").
		Where("i.agent_id = ? AND i.input->>'need_type' IN ?", owner, kinds).
		Where("n.normalized->'constraints'->>'deadline_ms' IS NULL OR (n.normalized->'constraints'->>'deadline_ms')::bigint > ?", now).
		Order("COALESCE((i.input->>'priority')::double precision, 0) DESC, i.created_at DESC, i.need_input_id ASC").Limit(5).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]Snapshot, 0, len(rows))
	for _, row := range rows {
		snapshot, err := row.snapshot()
		if err != nil {
			return nil, err
		}
		out = append(out, snapshot)
	}
	return out, nil
}

// CheckIntent authorizes an inline execution without creating another input.
func (s Store) CheckIntent(ctx context.Context, owner, intentID, version int64) error {
	var exists bool
	err := s.DB.WithContext(ctx).Raw(`SELECT EXISTS (SELECT 1 FROM agent_intent_actions
 WHERE agent_id = ? AND intent_id = ? AND version = ? AND status = 'active')`, owner, intentID, version).Scan(&exists).Error
	if err != nil {
		return err
	}
	if !exists {
		return ErrStaleIntent
	}
	return nil
}
