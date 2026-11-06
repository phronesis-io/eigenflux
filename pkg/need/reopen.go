package need

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"unicode/utf8"

	"gorm.io/gorm"
)

var ErrStaleCapture = errors.New("capture revision is no longer current")
var ErrReopenNotAllowed = errors.New("only a completed no_need review can be reopened")

// CaptureRevision fences submissions from earlier reviews of the same Intent version.
// State is pending, no_need or captured; pending is never a completed review.
type CaptureReviewState struct {
	IntentID        int64  `json:"intent_id,string"`
	IntentVersion   int64  `json:"intent_version"`
	CaptureRevision int64  `json:"capture_revision"`
	State           string `json:"state"`
	Reason          string `json:"reason,omitempty"`
}

type ReopenCaptureRequest struct {
	IntentID                int64  `json:"intent_id,string"`
	IntentVersion           int64  `json:"intent_version"`
	ExpectedCaptureRevision *int64 `json:"expected_capture_revision"`
}

func DecodeReopenCapture(raw []byte) (ReopenCaptureRequest, error) {
	var r ReopenCaptureRequest
	if len(raw) == 0 || len(raw) > 4096 || !json.Valid(raw) || !utf8.Valid(raw) {
		return r, invalid("review", "require_json_object_within_4_KiB")
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
	if json.Unmarshal(raw, &link) != nil || r.IntentID <= 0 || link.IntentID != strconv.FormatInt(r.IntentID, 10) || r.IntentVersion <= 0 {
		return r, invalid("intent", "invalid_link")
	}
	if r.ExpectedCaptureRevision == nil || *r.ExpectedCaptureRevision < 0 || *r.ExpectedCaptureRevision == math.MaxInt64 {
		return r, invalid("expected_capture_revision", "require_nonnegative_incrementable_integer")
	}
	return r, nil
}

func (s Store) CaptureReviewState(ctx context.Context, owner, intent, version int64) (CaptureReviewState, error) {
	var out CaptureReviewState
	if owner <= 0 || intent <= 0 || version <= 0 {
		return out, ErrStaleIntent
	}
	err := s.DB.WithContext(ctx).Raw(`SELECT a.intent_id,a.version AS intent_version,
 COALESCE(r.capture_revision,0) AS capture_revision,
 CASE WHEN r.intent_id IS NULL OR r.reopened_at IS NOT NULL THEN 'pending' ELSE r.outcome END AS state,
 CASE WHEN r.reopened_at IS NULL THEN COALESCE(r.reason,'') ELSE '' END AS reason
 FROM agent_intent_actions a LEFT JOIN need_capture_reviews r
 ON r.agent_id=a.agent_id AND r.intent_id=a.intent_id AND r.intent_version=a.version
 WHERE a.agent_id=? AND a.intent_id=? AND a.version=? AND a.status='active'`, owner, intent, version).Scan(&out).Error
	if err != nil {
		return out, err
	}
	if out.IntentID == 0 {
		return out, ErrStaleIntent
	}
	return out, nil
}

// ReopenCapture only enrolls one explicitly selected completed no_need. Repeated
// requests cannot reopen a later completion or erase the previous reason on failure.
func (s Store) ReopenCapture(ctx context.Context, owner int64, r ReopenCaptureRequest, now int64) (CaptureReviewState, bool, error) {
	var out CaptureReviewState
	replayed := false
	if owner <= 0 || r.IntentID <= 0 || r.IntentVersion <= 0 || r.ExpectedCaptureRevision == nil || *r.ExpectedCaptureRevision < 0 || *r.ExpectedCaptureRevision == math.MaxInt64 || now <= 0 {
		return out, false, invalid("review", "invalid_reopen")
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", owner).Error; err != nil {
			return err
		}
		var locked int64
		if err := tx.Raw(`SELECT intent_id FROM agent_intent_actions WHERE agent_id=? AND intent_id=? AND version=? AND status='active' FOR UPDATE`, owner, r.IntentID, r.IntentVersion).Scan(&locked).Error; err != nil {
			return err
		}
		if locked == 0 {
			return ErrStaleIntent
		}
		var err error
		out, err = (Store{DB: tx}).CaptureReviewState(ctx, owner, r.IntentID, r.IntentVersion)
		if err != nil {
			return err
		}
		expected := *r.ExpectedCaptureRevision
		if out.CaptureRevision == expected+1 {
			replayed = true
			return nil
		}
		if out.CaptureRevision != expected {
			return ErrStaleCapture
		}
		if out.State != "no_need" {
			return ErrReopenNotAllowed
		}
		if err := tx.Exec(`UPDATE need_capture_reviews SET capture_revision=capture_revision+1,reopened_at=? WHERE agent_id=? AND intent_id=? AND intent_version=?`, now, owner, r.IntentID, r.IntentVersion).Error; err != nil {
			return err
		}
		out.CaptureRevision++
		out.State = "pending"
		out.Reason = ""
		return nil
	})
	return out, replayed, err
}
