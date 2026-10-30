package dal

import (
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// ItemSlopEvaluation is a durable first-result checkpoint. Absence means unevaluated.
type ItemSlopEvaluation struct {
	ItemID           int64 `gorm:"primaryKey"`
	PromptVersion    string
	Model            string
	ContentHash      string
	TemplateHash     string
	RequestHash      string
	Response         string
	IsSlop           *int
	SlopReason       string
	SlopScore        *float64
	ModelQuality     *float64
	EffectiveQuality *float64
	Discard          bool
	Blocked          bool
	EvaluatedAt      int64
}

func (ItemSlopEvaluation) TableName() string { return "item_slop_evaluations" }

func GetSlopEvaluation(db *gorm.DB, id int64) (*ItemSlopEvaluation, error) {
	var row ItemSlopEvaluation
	err := db.Where("item_id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &row, err
}

// SaveSlopEvaluation atomically preserves the first decision and its blocking state.
// Concurrent attempts consume that decision instead of replacing it with a new model vote.
func SaveSlopEvaluation(db *gorm.DB, candidate *ItemSlopEvaluation) (*ItemSlopEvaluation, error) {
	var saved *ItemSlopEvaluation
	err := db.Transaction(func(tx *gorm.DB) error {
		candidate.EvaluatedAt = time.Now().UnixMilli()
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(candidate).Error; err != nil {
			return err
		}
		var err error
		saved, err = GetSlopEvaluation(tx, candidate.ItemID)
		if err != nil {
			return err
		}
		if saved == nil || saved.ContentHash != candidate.ContentHash || saved.PromptVersion != candidate.PromptVersion {
			return fmt.Errorf("slop checkpoint identity mismatch")
		}
		if saved.Blocked {
			// Keep the existing public rejection category; detailed reasons are internal.
			update := tx.Model(&ProcessedItem{}).Where("item_id = ? AND status NOT IN ?", candidate.ItemID, []int16{StatusDeleted, StatusCompleted}).Updates(map[string]any{"status": StatusDiscarded, "distribution_skip_reason": DistributionSkipContentEvaluation, "quality_score": 0, "updated_at": time.Now().UnixMilli()})
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected != 1 {
				return fmt.Errorf("slop checkpoint cannot block a missing, completed or deleted item")
			}
		}
		return nil
	})
	return saved, err
}
func IsSlopBlocked(db *gorm.DB, id int64) (bool, error) {
	row, err := GetSlopEvaluation(db, id)
	return row != nil && row.Blocked, err
}
