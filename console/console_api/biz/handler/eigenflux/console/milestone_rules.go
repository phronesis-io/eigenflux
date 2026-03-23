package console

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"console.eigenflux.ai/internal/dal"
	"console.eigenflux.ai/internal/db"
	"console.eigenflux.ai/internal/model"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"gorm.io/gorm"
)

type MilestoneRuleInfo struct {
	RuleID          string `json:"rule_id"`
	MetricKey       string `json:"metric_key"`
	Threshold       int64  `json:"threshold"`
	RuleEnabled     bool   `json:"rule_enabled"`
	ContentTemplate string `json:"content_template"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

type ListMilestoneRulesData struct {
	Rules    []MilestoneRuleInfo `json:"rules"`
	Total    int64               `json:"total"`
	Page     int32               `json:"page"`
	PageSize int32               `json:"page_size"`
}

type ListMilestoneRulesResp struct {
	Code int32                   `json:"code"`
	Msg  string                  `json:"msg"`
	Data *ListMilestoneRulesData `json:"data,omitempty"`
}

type MilestoneRuleData struct {
	Rule MilestoneRuleInfo `json:"rule"`
}

type MilestoneRuleResp struct {
	Code int32              `json:"code"`
	Msg  string             `json:"msg"`
	Data *MilestoneRuleData `json:"data,omitempty"`
}

type ReplaceMilestoneRuleData struct {
	OldRule MilestoneRuleInfo `json:"old_rule"`
	NewRule MilestoneRuleInfo `json:"new_rule"`
}

type ReplaceMilestoneRuleResp struct {
	Code int32                     `json:"code"`
	Msg  string                    `json:"msg"`
	Data *ReplaceMilestoneRuleData `json:"data,omitempty"`
}

type createMilestoneRuleReq struct {
	MetricKey       string `json:"metric_key"`
	Threshold       int64  `json:"threshold"`
	RuleEnabled     *bool  `json:"rule_enabled"`
	ContentTemplate string `json:"content_template"`
}

type updateMilestoneRuleReq struct {
	RuleEnabled     *bool   `json:"rule_enabled"`
	ContentTemplate *string `json:"content_template"`
}

type replaceMilestoneRuleReq struct {
	MetricKey       string `json:"metric_key"`
	Threshold       int64  `json:"threshold"`
	RuleEnabled     *bool  `json:"rule_enabled"`
	ContentTemplate string `json:"content_template"`
}

// ListMilestoneRules godoc
// @Summary      List milestone rules
// @Description  Returns a paginated list of milestone rules with optional filters
// @Tags         console
// @Produce      json
// @Param        page          query  integer  false  "Page number (default: 1)"
// @Param        page_size     query  integer  false  "Items per page (default: 20, max: 100)"
// @Param        metric_key    query  string   false  "Filter by metric key"
// @Param        rule_enabled  query  boolean  false  "Filter by enabled status"
// @Success      200           {object}  ListMilestoneRulesResp
// @Router       /console/api/v1/milestone-rules [get]
func ListMilestoneRules(ctx context.Context, c *app.RequestContext) {
	page, pageSize := parsePagination(c)
	metricKey := strings.TrimSpace(c.Query("metric_key"))

	var ruleEnabled *bool
	if raw := strings.TrimSpace(c.Query("rule_enabled")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeConsoleError(c, "invalid rule_enabled")
			return
		}
		ruleEnabled = &parsed
	}

	rules, total, err := dal.ListMilestoneRules(db.DB, dal.ListMilestoneRulesParams{
		Page:        page,
		PageSize:    pageSize,
		MetricKey:   metricKey,
		RuleEnabled: ruleEnabled,
	})
	if err != nil {
		writeConsoleError(c, "database query failed: "+err.Error())
		return
	}

	respRules := make([]MilestoneRuleInfo, 0, len(rules))
	for _, rule := range rules {
		respRules = append(respRules, toMilestoneRuleInfo(rule))
	}

	c.JSON(consts.StatusOK, &ListMilestoneRulesResp{
		Code: 0,
		Msg:  "success",
		Data: &ListMilestoneRulesData{
			Rules:    respRules,
			Total:    total,
			Page:     page,
			PageSize: pageSize,
		},
	})
}

// CreateMilestoneRule godoc
// @Summary      Create milestone rule
// @Description  Creates a new milestone rule
// @Tags         console
// @Accept       json
// @Produce      json
// @Param        body  body      createMilestoneRuleReq  true  "Create milestone rule request"
// @Success      200   {object}  MilestoneRuleResp
// @Router       /console/api/v1/milestone-rules [post]
func CreateMilestoneRule(ctx context.Context, c *app.RequestContext) {
	var req createMilestoneRuleReq
	if err := c.BindAndValidate(&req); err != nil {
		writeConsoleError(c, "invalid request: "+err.Error())
		return
	}

	ruleEnabled := true
	if req.RuleEnabled != nil {
		ruleEnabled = *req.RuleEnabled
	}

	rule, err := dal.CreateMilestoneRule(ctx, db.DB, dal.CreateMilestoneRuleParams{
		MetricKey:       strings.TrimSpace(req.MetricKey),
		Threshold:       req.Threshold,
		RuleEnabled:     ruleEnabled,
		ContentTemplate: req.ContentTemplate,
	})
	if err != nil {
		writeRuleMutationError(c, err)
		return
	}

	c.JSON(consts.StatusOK, &MilestoneRuleResp{
		Code: 0,
		Msg:  "success",
		Data: &MilestoneRuleData{Rule: toMilestoneRuleInfo(*rule)},
	})
}

// UpdateMilestoneRule godoc
// @Summary      Update milestone rule
// @Description  Updates rule_enabled and/or content_template of an existing milestone rule
// @Tags         console
// @Accept       json
// @Produce      json
// @Param        rule_id  path      integer                 true  "Rule ID"
// @Param        body     body      updateMilestoneRuleReq  true  "Update milestone rule request"
// @Success      200      {object}  MilestoneRuleResp
// @Router       /console/api/v1/milestone-rules/{rule_id} [put]
func UpdateMilestoneRule(ctx context.Context, c *app.RequestContext) {
	ruleID, ok := parseRuleID(c)
	if !ok {
		return
	}

	var req updateMilestoneRuleReq
	if err := c.BindAndValidate(&req); err != nil {
		writeConsoleError(c, "invalid request: "+err.Error())
		return
	}
	if req.RuleEnabled == nil && req.ContentTemplate == nil {
		writeConsoleError(c, "at least one field must be provided")
		return
	}

	rule, err := dal.UpdateMilestoneRule(ctx, db.DB, ruleID, dal.UpdateMilestoneRuleParams{
		RuleEnabled:     req.RuleEnabled,
		ContentTemplate: req.ContentTemplate,
	})
	if err != nil {
		writeRuleMutationError(c, err)
		return
	}

	c.JSON(consts.StatusOK, &MilestoneRuleResp{
		Code: 0,
		Msg:  "success",
		Data: &MilestoneRuleData{Rule: toMilestoneRuleInfo(*rule)},
	})
}

// ReplaceMilestoneRule godoc
// @Summary      Replace milestone rule
// @Description  Disables an existing milestone rule and creates a new replacement rule
// @Tags         console
// @Accept       json
// @Produce      json
// @Param        rule_id  path      integer                  true  "Rule ID"
// @Param        body     body      replaceMilestoneRuleReq  true  "Replace milestone rule request"
// @Success      200      {object}  ReplaceMilestoneRuleResp
// @Router       /console/api/v1/milestone-rules/{rule_id}/replace [post]
func ReplaceMilestoneRule(ctx context.Context, c *app.RequestContext) {
	ruleID, ok := parseRuleID(c)
	if !ok {
		return
	}

	var req replaceMilestoneRuleReq
	if err := c.BindAndValidate(&req); err != nil {
		writeConsoleError(c, "invalid request: "+err.Error())
		return
	}

	ruleEnabled := true
	if req.RuleEnabled != nil {
		ruleEnabled = *req.RuleEnabled
	}

	oldRule, newRule, err := dal.ReplaceMilestoneRule(ctx, db.DB, ruleID, dal.ReplaceMilestoneRuleParams{
		MetricKey:       strings.TrimSpace(req.MetricKey),
		Threshold:       req.Threshold,
		RuleEnabled:     ruleEnabled,
		ContentTemplate: req.ContentTemplate,
	})
	if err != nil {
		writeRuleMutationError(c, err)
		return
	}

	c.JSON(consts.StatusOK, &ReplaceMilestoneRuleResp{
		Code: 0,
		Msg:  "success",
		Data: &ReplaceMilestoneRuleData{
			OldRule: toMilestoneRuleInfo(*oldRule),
			NewRule: toMilestoneRuleInfo(*newRule),
		},
	})
}

func parsePagination(c *app.RequestContext) (int32, int32) {
	page := int32(1)
	pageSize := int32(20)

	if raw := strings.TrimSpace(c.Query("page")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 32); err == nil && parsed > 0 {
			page = int32(parsed)
		}
	}
	if raw := strings.TrimSpace(c.Query("page_size")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 32); err == nil && parsed > 0 {
			pageSize = int32(parsed)
		}
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func parseRuleID(c *app.RequestContext) (int64, bool) {
	ruleID, err := strconv.ParseInt(strings.TrimSpace(c.Param("rule_id")), 10, 64)
	if err != nil || ruleID <= 0 {
		writeConsoleError(c, "invalid rule_id")
		return 0, false
	}
	return ruleID, true
}

func writeConsoleError(c *app.RequestContext, msg string) {
	c.JSON(consts.StatusOK, map[string]interface{}{
		"code": 1,
		"msg":  msg,
	})
}

func writeRuleMutationError(c *app.RequestContext, err error) {
	switch {
	case errors.Is(err, dal.ErrRuleNotFound):
		writeConsoleError(c, err.Error())
	case errors.Is(err, gorm.ErrDuplicatedKey):
		writeConsoleError(c, "milestone rule already exists")
	default:
		writeConsoleError(c, err.Error())
	}
}

func toMilestoneRuleInfo(rule model.MilestoneRule) MilestoneRuleInfo {
	return MilestoneRuleInfo{
		RuleID:          strconv.FormatInt(rule.RuleID, 10),
		MetricKey:       rule.MetricKey,
		Threshold:       rule.Threshold,
		RuleEnabled:     rule.RuleEnabled,
		ContentTemplate: rule.ContentTemplate,
		CreatedAt:       rule.CreatedAt,
		UpdatedAt:       rule.UpdatedAt,
	}
}
