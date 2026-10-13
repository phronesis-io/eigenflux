package need

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
)

const MaxReviewBytes = 100 << 10

// CaptureReview completes one Agent interpretation of a confirmed Intent version.
// Inputs contains at most one missing input per kind; existing inputs stay intact.
type CaptureReview struct {
	IntentID      int64             `json:"intent_id,string"`
	IntentVersion int64             `json:"intent_version"`
	Outcome       string            `json:"outcome"`
	Reason        string            `json:"reason,omitempty"`
	Inputs        []json.RawMessage `json:"inputs"`
}

type PendingCapture struct {
	IntentID          int64           `json:"intent_id,string"`
	IntentVersion     int64           `json:"intent_version"`
	WatchFor          string          `json:"watch_for"`
	TriggerWhen       string          `json:"trigger_when"`
	ActionInstruction string          `json:"action_instruction"`
	ActionPolicy      string          `json:"action_policy"`
	Priority          int16           `json:"priority"`
	ExistingInputs    json.RawMessage `json:"existing_inputs" gorm:"column:existing_inputs"`
}

type PendingCaptures struct {
	Intents []PendingCapture `json:"intents"`
	HasMore bool             `json:"has_more"`
}

func DecodeCaptureReview(raw []byte) (CaptureReview, error) {
	var r CaptureReview
	if len(raw) == 0 || len(raw) > MaxReviewBytes || !json.Valid(raw) || !utf8.Valid(raw) {
		return r, invalid("review", "require_json_object_within_100_KiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := checkJSON(d); err != nil {
		return r, err
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return r, invalid("review", "invalid_fields")
	}
	var link struct {
		IntentID string `json:"intent_id"`
	}
	if err := json.Unmarshal(raw, &link); err != nil || link.IntentID != strconv.FormatInt(r.IntentID, 10) {
		return r, invalid("intent_id", "canonical_positive_int64_required")
	}
	if r.Inputs == nil {
		return r, invalid("inputs", "require_array")
	}
	if r.IntentID <= 0 || r.IntentVersion <= 0 {
		return r, invalid("intent", "invalid_link")
	}
	if r.Outcome != "captured" && r.Outcome != "no_need" {
		return r, invalid("outcome", "require_captured_or_no_need")
	}
	if len(r.Reason) > 2000 || r.Outcome == "no_need" && strings.TrimSpace(r.Reason) == "" {
		return r, invalid("reason", "require_bounded_no_need_explanation")
	}
	if len(r.Inputs) > 3 || r.Outcome == "no_need" && len(r.Inputs) != 0 {
		return r, invalid("inputs", "invalid_count")
	}
	seen := map[string]bool{}
	for _, raw := range r.Inputs {
		in, err := Decode(raw)
		if err != nil {
			return r, err
		}
		if in.IntentID != r.IntentID || in.IntentVersion != r.IntentVersion {
			return r, invalid("inputs", "intent_mismatch")
		}
		if seen[in.NeedType] {
			return r, invalid("inputs", "duplicate_kind")
		}
		seen[in.NeedType] = true
	}
	return r, nil
}

// PendingCaptures derives work from current active Intents, so old accounts and
// new Intent revisions require no enqueue job. Completion is shared across Homes.
func (s Store) PendingCaptures(ctx context.Context, owner int64, limit int) (PendingCaptures, error) {
	out := PendingCaptures{Intents: []PendingCapture{}}
	if owner <= 0 || limit < 1 || limit > 10 {
		return out, invalid("limit", "require_1_to_10")
	}
	err := s.DB.WithContext(ctx).Raw(`SELECT a.intent_id, a.version AS intent_version,
 a.watch_for, a.trigger_when, a.action_instruction, a.action_policy, a.priority,
 COALESCE((SELECT jsonb_agg(jsonb_build_object('need_input_id',n.need_input_id::text,
 'need_type',n.input->>'need_type') ORDER BY n.need_input_id)
 FROM current_need_inputs n WHERE n.agent_id=a.agent_id AND n.intent_id=a.intent_id), '[]'::jsonb) AS existing_inputs
 FROM agent_intent_actions a WHERE a.agent_id=? AND a.status='active'
 AND NOT EXISTS (SELECT 1 FROM need_capture_reviews r WHERE r.agent_id=a.agent_id
 AND r.intent_id=a.intent_id AND r.intent_version=a.version)
 ORDER BY a.priority DESC, a.intent_id ASC LIMIT ?`, owner, limit+1).Scan(&out.Intents).Error
	if len(out.Intents) > limit {
		out.HasMore = true
		out.Intents = out.Intents[:limit]
	}
	return out, err
}

// CompleteCapture commits all new inputs and the review together. A failed or
// interrupted attempt stays pending; identical retries do not create more Needs.
func (s Store) CompleteCapture(ctx context.Context, owner int64, raw []byte, now int64) (bool, error) {
	r, err := DecodeCaptureReview(raw)
	if err != nil {
		return false, err
	}
	if owner <= 0 {
		return false, invalid("owner", "invalid")
	}
	normalized, err := json.Marshal(r)
	if err != nil {
		return false, err
	}
	_, hash, err := canonical(normalized)
	if err != nil {
		return false, err
	}
	replayed := false
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Match Create's lock order, including concurrent direct captures.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", owner).Error; err != nil {
			return err
		}
		var intent struct {
			Version int64
			Status  string
		}
		if err := tx.Raw(`SELECT version,status FROM agent_intent_actions WHERE agent_id=? AND intent_id=? FOR UPDATE`, owner, r.IntentID).Scan(&intent).Error; err != nil {
			return err
		}
		if intent.Status != "active" || intent.Version != r.IntentVersion {
			return ErrStaleIntent
		}
		var previous string
		if err := tx.Raw(`SELECT request_hash FROM need_capture_reviews WHERE agent_id=? AND intent_id=? AND intent_version=?`, owner, r.IntentID, r.IntentVersion).Scan(&previous).Error; err != nil {
			return err
		}
		if previous != "" {
			if previous != hash {
				return ErrConflict
			}
			replayed = true
			return nil
		}
		var kinds []string
		if err := tx.Raw(`SELECT DISTINCT input->>'need_type' FROM current_need_inputs WHERE agent_id=? AND intent_id=?`, owner, r.IntentID).Scan(&kinds).Error; err != nil {
			return err
		}
		existing := map[string]bool{}
		for _, k := range kinds {
			existing[k] = true
		}
		if r.Outcome == "no_need" && len(kinds) > 0 {
			return invalid("outcome", "existing_inputs_require_captured")
		}
		if r.Outcome == "captured" && len(kinds)+len(r.Inputs) == 0 {
			return invalid("inputs", "captured_requires_input")
		}
		for _, rawInput := range r.Inputs {
			in, _ := Decode(rawInput) // Validated before starting the transaction.
			if existing[in.NeedType] {
				return invalid("inputs", "kind_already_captured_refresh_pending")
			}
			key := fmt.Sprintf("intent-review-v2-%d-%d-%s", r.IntentID, r.IntentVersion, in.NeedType)
			if _, _, err := (Store{DB: tx, IDs: s.IDs}).Create(ctx, owner, key, rawInput, now); err != nil {
				return err
			}
		}
		return tx.Exec(`INSERT INTO need_capture_reviews(agent_id,intent_id,intent_version,outcome,reason,request_hash,completed_at)
 VALUES (?,?,?,?,?,?,?)`, owner, r.IntentID, r.IntentVersion, r.Outcome, r.Reason, hash, now).Error
	})
	return replayed, err
}
