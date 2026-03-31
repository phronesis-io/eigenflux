package dal

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"text/template"
	"time"

	"console.eigenflux.ai/internal/db"
	"console.eigenflux.ai/internal/milestone"
	"console.eigenflux.ai/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrRuleNotFound = errors.New("milestone rule not found")

type ListMilestoneRulesParams struct {
	Page        int32
	PageSize    int32
	MetricKey   string
	RuleEnabled *bool
}

type CreateMilestoneRuleParams struct {
	MetricKey       string
	Threshold       int64
	RuleEnabled     bool
	ContentTemplate string
}

type UpdateMilestoneRuleParams struct {
	RuleEnabled     *bool
	ContentTemplate *string
}

type ReplaceMilestoneRuleParams struct {
	MetricKey       string
	Threshold       int64
	RuleEnabled     bool
	ContentTemplate string
}

func ListMilestoneRules(gormDB *gorm.DB, params ListMilestoneRulesParams) ([]model.MilestoneRule, int64, error) {
	var rules []model.MilestoneRule
	var total int64

	query := gormDB.Model(&model.MilestoneRule{})
	if params.MetricKey != "" {
		query = query.Where("metric_key = ?", params.MetricKey)
	}
	if params.RuleEnabled != nil {
		query = query.Where("rule_enabled = ?", *params.RuleEnabled)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	offset := (params.Page - 1) * params.PageSize
	err := query.
		Order("metric_key ASC, threshold ASC, rule_id ASC").
		Offset(int(offset)).
		Limit(int(params.PageSize)).
		Find(&rules).Error
	if err != nil {
		return nil, 0, err
	}
	return rules, total, nil
}

func CreateMilestoneRule(ctx context.Context, gormDB *gorm.DB, params CreateMilestoneRuleParams) (*model.MilestoneRule, error) {
	if err := validateMilestoneRuleInput(params.MetricKey, params.Threshold, params.ContentTemplate); err != nil {
		return nil, err
	}

	now := time.Now().UnixMilli()
	rule := &model.MilestoneRule{
		MetricKey:       params.MetricKey,
		Threshold:       params.Threshold,
		RuleEnabled:     params.RuleEnabled,
		ContentTemplate: strings.TrimSpace(params.ContentTemplate),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := gormDB.WithContext(ctx).Create(rule).Error; err != nil {
		return nil, err
	}
	publishRuleInvalidations(ctx, rule.MetricKey)
	return rule, nil
}

func UpdateMilestoneRule(ctx context.Context, gormDB *gorm.DB, ruleID int64, params UpdateMilestoneRuleParams) (*model.MilestoneRule, error) {
	var rule model.MilestoneRule
	if err := gormDB.WithContext(ctx).Take(&rule, "rule_id = ?", ruleID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRuleNotFound
		}
		return nil, err
	}

	updates := map[string]interface{}{
		"updated_at": time.Now().UnixMilli(),
	}
	if params.RuleEnabled != nil {
		updates["rule_enabled"] = *params.RuleEnabled
	}
	if params.ContentTemplate != nil {
		content := strings.TrimSpace(*params.ContentTemplate)
		if content == "" {
			return nil, errors.New("content_template is required")
		}
		if err := validateContentTemplate(content); err != nil {
			return nil, err
		}
		updates["content_template"] = content
	}

	if err := gormDB.WithContext(ctx).
		Model(&model.MilestoneRule{}).
		Where("rule_id = ?", ruleID).
		Updates(updates).Error; err != nil {
		return nil, err
	}

	if err := gormDB.WithContext(ctx).Take(&rule, "rule_id = ?", ruleID).Error; err != nil {
		return nil, err
	}
	publishRuleInvalidations(ctx, rule.MetricKey)
	return &rule, nil
}

func ReplaceMilestoneRule(ctx context.Context, gormDB *gorm.DB, ruleID int64, params ReplaceMilestoneRuleParams) (*model.MilestoneRule, *model.MilestoneRule, error) {
	if err := validateMilestoneRuleInput(params.MetricKey, params.Threshold, params.ContentTemplate); err != nil {
		return nil, nil, err
	}

	var oldRule model.MilestoneRule
	var newRule model.MilestoneRule
	err := gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&oldRule, "rule_id = ?", ruleID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRuleNotFound
			}
			return err
		}

		now := time.Now().UnixMilli()
		if err := tx.Model(&model.MilestoneRule{}).
			Where("rule_id = ?", ruleID).
			Updates(map[string]interface{}{
				"rule_enabled": false,
				"updated_at":   now,
			}).Error; err != nil {
			return err
		}

		newRule = model.MilestoneRule{
			MetricKey:       params.MetricKey,
			Threshold:       params.Threshold,
			RuleEnabled:     params.RuleEnabled,
			ContentTemplate: strings.TrimSpace(params.ContentTemplate),
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		return tx.Create(&newRule).Error
	})
	if err != nil {
		return nil, nil, err
	}

	oldRule.RuleEnabled = false
	publishRuleInvalidations(ctx, oldRule.MetricKey, newRule.MetricKey)
	return &oldRule, &newRule, nil
}

func validateMilestoneRuleInput(metricKey string, threshold int64, contentTemplate string) error {
	if !milestone.IsValidMetricKey(metricKey) {
		return errors.New("invalid metric_key")
	}
	if threshold <= 0 {
		return errors.New("threshold must be greater than 0")
	}
	content := strings.TrimSpace(contentTemplate)
	if content == "" {
		return errors.New("content_template is required")
	}
	return validateContentTemplate(content)
}

func validateContentTemplate(contentTemplate string) error {
	_, err := template.New("content_template").Option("missingkey=error").Parse(contentTemplate)
	return err
}

func publishRuleInvalidations(ctx context.Context, metricKeys ...string) {
	if db.RDB == nil {
		return
	}
	if err := milestone.PublishRuleInvalidations(ctx, db.RDB, metricKeys...); err != nil {
		slog.Warn("publish milestone rule invalidation failed", "metrics", metricKeys, "err", err)
	}
}
