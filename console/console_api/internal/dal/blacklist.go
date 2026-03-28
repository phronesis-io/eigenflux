package dal

import (
	"context"
	"errors"
	"strings"
	"time"

	"console.eigenflux.ai/internal/model"

	"gorm.io/gorm"
)

var ErrBlacklistKeywordNotFound = errors.New("blacklist keyword not found")

type ListBlacklistKeywordsParams struct {
	Page     int32
	PageSize int32
	Enabled  *bool
}

func ListBlacklistKeywords(gormDB *gorm.DB, params ListBlacklistKeywordsParams) ([]model.BlacklistKeyword, int64, error) {
	var rows []model.BlacklistKeyword
	var total int64

	query := gormDB.Model(&model.BlacklistKeyword{})
	if params.Enabled != nil {
		query = query.Where("enabled = ?", *params.Enabled)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	offset := (params.Page - 1) * params.PageSize
	err := query.
		Order("created_at DESC, keyword_id DESC").
		Offset(int(offset)).
		Limit(int(params.PageSize)).
		Find(&rows).Error
	return rows, total, err
}

func CreateBlacklistKeyword(ctx context.Context, gormDB *gorm.DB, keyword string) (*model.BlacklistKeyword, error) {
	now := time.Now().UnixMilli()
	row := &model.BlacklistKeyword{
		Keyword:   strings.ToLower(keyword),
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := gormDB.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

func UpdateBlacklistKeyword(ctx context.Context, gormDB *gorm.DB, keywordID int64, enabled *bool) (*model.BlacklistKeyword, error) {
	var row model.BlacklistKeyword
	if err := gormDB.WithContext(ctx).Take(&row, "keyword_id = ?", keywordID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBlacklistKeywordNotFound
		}
		return nil, err
	}

	updates := map[string]interface{}{
		"updated_at": time.Now().UnixMilli(),
	}
	if enabled != nil {
		updates["enabled"] = *enabled
	}

	if err := gormDB.WithContext(ctx).
		Model(&model.BlacklistKeyword{}).
		Where("keyword_id = ?", keywordID).
		Updates(updates).Error; err != nil {
		return nil, err
	}

	if err := gormDB.WithContext(ctx).Take(&row, "keyword_id = ?", keywordID).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func DeleteBlacklistKeyword(ctx context.Context, gormDB *gorm.DB, keywordID int64) error {
	result := gormDB.WithContext(ctx).Where("keyword_id = ?", keywordID).Delete(&model.BlacklistKeyword{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrBlacklistKeywordNotFound
	}
	return nil
}

// GetEnabledKeywords returns all enabled keyword strings. Used by pipeline cache.
func GetEnabledKeywords(gormDB *gorm.DB) ([]string, error) {
	var keywords []string
	err := gormDB.Model(&model.BlacklistKeyword{}).
		Where("enabled = ?", true).
		Pluck("keyword", &keywords).Error
	return keywords, err
}
