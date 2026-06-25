package install

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// ErrTokenNotFound is returned by ReportInstall when the token doesn't exist.
var ErrTokenNotFound = errors.New("token not found")

// CreateToken inserts a freshly minted token row.
func CreateToken(db *gorm.DB, t *Token) error {
	if t.CreatedAt == 0 {
		t.CreatedAt = time.Now().UnixMilli()
	}
	if t.Status == "" {
		t.Status = StatusPending
	}
	return db.Create(t).Error
}

// MarkFetched stamps fetched_at the first time the /r/<ref> bootstrap is read
// (idempotent: only the first fetch sets it). Returns the row, or (nil, nil)
// when the ref doesn't exist.
func MarkFetched(db *gorm.DB, ref string) (*Token, error) {
	if err := db.Model(&Token{}).
		Where("token = ? AND fetched_at = 0", ref).
		Update("fetched_at", time.Now().UnixMilli()).Error; err != nil {
		return nil, err
	}
	var t Token
	err := db.Where("token = ?", ref).First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ClaimCallback atomically claims the right to send the one platform conversion
// callback for ref: it sets callback_sent_at if still 0 and reports whether this
// caller won the claim, along with the ref's click_id/twclid. Mirrors the
// RowsAffected-as-lock pattern, so concurrent triggers (/r/ fetch and install
// report) can't double-report the same click. Returns won=false when the ref is
// absent, has no platform click id, or was already claimed.
func ClaimCallback(db *gorm.DB, ref string) (won bool, t *Token, err error) {
	res := db.Model(&Token{}).
		Where("token = ? AND callback_sent_at = 0 AND (click_id <> '' OR twclid <> '')", ref).
		Update("callback_sent_at", time.Now().UnixMilli())
	if res.Error != nil {
		return false, nil, res.Error
	}
	if res.RowsAffected == 0 {
		return false, nil, nil
	}
	var loaded Token
	if err := db.Where("token = ?", ref).First(&loaded).Error; err != nil {
		return false, nil, err
	}
	return true, &loaded, nil
}

// ReportInstall records one report hit for token and returns whether this call
// was the conversion (the first report). The pending->installed flip is a
// single conditional UPDATE (the same RowsAffected-as-lock pattern as
// agti.LockAgentAnswers), so concurrent reports can't double-count a
// conversion. report_count is incremented on every hit for raw observability.
// Returns ErrTokenNotFound when the token doesn't exist.
func ReportInstall(db *gorm.DB, token string) (converted bool, t *Token, err error) {
	now := time.Now().UnixMilli()
	err = db.Transaction(func(tx *gorm.DB) error {
		// Atomic conversion flip: matches at most one row (the pending one).
		flip := tx.Model(&Token{}).
			Where("token = ? AND status = ?", token, StatusPending).
			Updates(map[string]interface{}{
				"status":       StatusInstalled,
				"reported_at":  now,
				"report_count": gorm.Expr("report_count + 1"),
			})
		if flip.Error != nil {
			return flip.Error
		}
		converted = flip.RowsAffected == 1
		if !converted {
			// Already installed (or otherwise non-pending): bump the raw
			// counter only. Zero rows here means the token doesn't exist.
			bump := tx.Model(&Token{}).
				Where("token = ?", token).
				Update("report_count", gorm.Expr("report_count + 1"))
			if bump.Error != nil {
				return bump.Error
			}
			if bump.RowsAffected == 0 {
				return ErrTokenNotFound
			}
		}
		var loaded Token
		if err := tx.Where("token = ?", token).First(&loaded).Error; err != nil {
			return err
		}
		t = &loaded
		return nil
	})
	if err != nil {
		return false, nil, err
	}
	return converted, t, nil
}
