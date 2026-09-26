package need

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Snapshot preserves the original Agent-authored JSON and its current Intent link.
// Execution never depends on a historical normalization projection.
type Snapshot struct {
	InputID       int64           `json:"need_input_id,string"`
	IntentID      int64           `json:"intent_id,string"`
	IntentVersion int64           `json:"intent_version"`
	Input         json.RawMessage `json:"input"`
}

type snapshotRow struct {
	InputID, IntentID, IntentVersion int64
	Input                            json.RawMessage
	Eligible                         bool
}

const snapshotColumns = `i.need_input_id AS input_id, i.intent_id, i.intent_version, i.input`

func (r snapshotRow) snapshot() (Snapshot, error) {
	link, err := decodeCompatible(r.Input)
	if err != nil {
		return Snapshot{}, fmt.Errorf("stored NeedInput: %w", err)
	}
	if link.IntentID != r.IntentID || link.IntentVersion != r.IntentVersion {
		return Snapshot{}, fmt.Errorf("stored NeedInput source mismatch")
	}
	return Snapshot{InputID: r.InputID, IntentID: r.IntentID, IntentVersion: r.IntentVersion, Input: r.Input}, nil
}

// ExecutionInput mechanically reads either protocol into current execution fields.
// It does not modify the source JSON or infer vocabulary, requirements or codes.
func (s Snapshot) ExecutionInput() (Input, error) {
	var link inputLink
	if err := json.Unmarshal(s.Input, &link); err != nil {
		return Input{}, err
	}
	if link.SchemaVersion != "need_input.v1" {
		return Decode(s.Input)
	}
	old, err := decodeLegacy(s.Input)
	if err != nil {
		return Input{}, err
	}
	in := Input{SchemaVersion: InputSchemaVersion, IntentID: old.IntentID, IntentVersion: old.IntentVersion,
		NeedType: old.NeedType, Target: Target{Goal: old.Target.Desc, Context: strings.Join(old.Target.CandidateNeeds, "\n")}, Priority: old.Priority, Constraints: old.Constraints}
	if old.Preferences != "" {
		in.Preferences = []Condition{{Text: old.Preferences}}
	}
	return in, nil
}

// ExecutionConstraints formats standard codes without interpreting natural-language
// aliases. One unknown alternative prevents the entire restriction from passing.
func ExecutionConstraints(c Constraints) (Constraints, bool) {
	out := c
	out.Lang = nil
	out.ProviderRegion = nil
	for _, value := range c.Lang {
		code, ok := languageCode(value)
		if !ok {
			return c, false
		}
		out.Lang = append(out.Lang, code)
	}
	for _, value := range c.ProviderRegion {
		code, ok := regionCode(value)
		if !ok {
			return c, false
		}
		out.ProviderRegion = append(out.ProviderRegion, code)
	}
	return out, true
}

// Current reads ownership and input eligibility in one MVCC statement.
func (s Store) Current(ctx context.Context, owner, inputID int64) (Snapshot, error) {
	var row snapshotRow
	err := s.DB.WithContext(ctx).Table("need_inputs i").Select(snapshotColumns+", n.need_input_id IS NOT NULL AS eligible").
		Joins("LEFT JOIN current_need_inputs n ON n.need_input_id = i.need_input_id").
		Where("i.agent_id = ? AND i.need_input_id = ?", owner, inputID).Scan(&row).Error
	if err != nil {
		return Snapshot{}, err
	}
	if row.InputID == 0 {
		return Snapshot{}, ErrNotFound
	}
	if !row.Eligible {
		return Snapshot{}, ErrStaleIntent
	}
	return row.snapshot()
}

// Active selects at most five matching Needs before any embedding/recall work.
// Only source lifecycle and deadline gate selection; candidate evidence is separate.
func (s Store) Active(ctx context.Context, owner int64, kinds []string, now int64) ([]Snapshot, error) {
	var rows []snapshotRow
	err := s.DB.WithContext(ctx).Table("current_need_inputs i").Select(snapshotColumns).
		Where("i.agent_id = ? AND i.input->>'need_type' IN ?", owner, kinds).
		Where("i.input->'constraints'->>'deadline_ms' IS NULL OR (i.input->'constraints'->>'deadline_ms')::bigint > ?", now).
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
