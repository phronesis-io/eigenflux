package console

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"console.eigenflux.ai/internal/dal"
	"console.eigenflux.ai/internal/db"
	"console.eigenflux.ai/internal/idgen"
	"console.eigenflux.ai/internal/model"
	"console.eigenflux.ai/internal/notification"
	console "console.eigenflux.ai/model/eigenflux/console"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"gorm.io/gorm"
)

// Keep hz-generated model import referenced to avoid unused import after hz update.
var _ = console.NewListAgentsReq

// ---------------------------------------------------------------------------
// Notification service wiring
// ---------------------------------------------------------------------------

var notifIDGen idgen.IDGenerator

func InitNotificationService(gen idgen.IDGenerator) {
	notifIDGen = gen
}

func notifService() *notification.Service {
	return notification.NewService(db.DB, db.RDB)
}

// ---------------------------------------------------------------------------
// Response types: Agents / Items / Impr
// ---------------------------------------------------------------------------

type ListAgentImprItemsData struct {
	AgentID  string                   `json:"agent_id"`
	ItemIDs  []string                 `json:"item_ids"`
	GroupIDs []string                 `json:"group_ids"`
	URLs     []string                 `json:"urls"`
	Items    []map[string]interface{} `json:"items"`
}

type ListAgentImprItemsResp struct {
	Code int32                   `json:"code"`
	Msg  string                  `json:"msg"`
	Data *ListAgentImprItemsData `json:"data,omitempty"`
}

// ---------------------------------------------------------------------------
// Response types: Milestone Rules
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Response types: System Notifications
// ---------------------------------------------------------------------------

type SystemNotificationInfo struct {
	NotificationID     string `json:"notification_id"`
	Type               string `json:"type"`
	Content            string `json:"content"`
	Status             int32  `json:"status"`
	AudienceType       string `json:"audience_type"`
	AudienceExpression string `json:"audience_expression"`
	StartAt            int64  `json:"start_at"`
	EndAt              int64  `json:"end_at"`
	OfflineAt          int64  `json:"offline_at"`
	CreatedAt          int64  `json:"created_at"`
	UpdatedAt          int64  `json:"updated_at"`
}

type ListSystemNotificationsData struct {
	Notifications []SystemNotificationInfo `json:"notifications"`
	Total         int64                    `json:"total"`
	Page          int32                    `json:"page"`
	PageSize      int32                    `json:"page_size"`
}

type ListSystemNotificationsResp struct {
	Code int32                        `json:"code"`
	Msg  string                       `json:"msg"`
	Data *ListSystemNotificationsData `json:"data,omitempty"`
}

type SystemNotificationData struct {
	Notification SystemNotificationInfo `json:"notification"`
}

type SystemNotificationResp struct {
	Code int32                   `json:"code"`
	Msg  string                  `json:"msg"`
	Data *SystemNotificationData `json:"data,omitempty"`
}

type createSystemNotificationReq struct {
	Type    string `json:"type"`
	Content string `json:"content"`
	Status  *int32 `json:"status"`
	StartAt *int64 `json:"start_at"`
	EndAt   *int64 `json:"end_at"`
}

type updateSystemNotificationReq struct {
	Type    *string `json:"type"`
	Content *string `json:"content"`
	Status  *int32  `json:"status"`
	StartAt *int64  `json:"start_at"`
	EndAt   *int64  `json:"end_at"`
}

// ===========================================================================
// Handlers: Agents
// ===========================================================================

// ListAgents godoc
// @Summary      List agents
// @Description  Returns a paginated list of agents with optional filters
// @Tags         console
// @Produce      json
// @Param        page       query  integer  false  "Page number (default: 1)"
// @Param        page_size  query  integer  false  "Items per page (default: 20, max: 100)"
// @Param        email      query  string   false  "Filter by email"
// @Param        name       query  string   false  "Search by agent name (partial match)"
// @Success      200  {object}  ListAgentsDocResp
// @Router /console/api/v1/agents [GET]
func ListAgents(ctx context.Context, c *app.RequestContext) {
	page, pageSize := parsePagination(c)
	email := strPtr(strings.TrimSpace(c.Query("email")))
	name := strPtr(strings.TrimSpace(c.Query("name")))

	agents, total, err := dal.ListAgents(db.DB, dal.ListAgentsParams{
		Page:      page,
		PageSize:  pageSize,
		Email:     email,
		AgentName: name,
	})
	if err != nil {
		writeConsoleError(c, "database query failed: "+err.Error())
		return
	}

	agentInfos := make([]map[string]interface{}, 0, len(agents))
	for _, a := range agents {
		info := map[string]interface{}{
			"agent_id":   strconv.FormatInt(a.AgentID, 10),
			"email":      a.Email,
			"agent_name": a.AgentName,
			"bio":        a.Bio,
			"created_at": a.CreatedAt,
			"updated_at": a.UpdatedAt,
		}
		if a.ProfileStatus != nil {
			info["profile_status"] = int32(*a.ProfileStatus)
		}
		if a.ProfileKeywords != nil && *a.ProfileKeywords != "" {
			info["profile_keywords"] = strings.Split(*a.ProfileKeywords, ",")
		}
		agentInfos = append(agentInfos, info)
	}

	c.JSON(consts.StatusOK, map[string]interface{}{
		"code": 0,
		"msg":  "success",
		"data": map[string]interface{}{
			"agents":    agentInfos,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		},
	})
}

// ===========================================================================
// Handlers: Items
// ===========================================================================

// ListItems godoc
// @Summary      List items
// @Description  Returns a paginated list of items with optional filters
// @Tags         console
// @Produce      json
// @Param        page       query  integer  false  "Page number (default: 1)"
// @Param        page_size  query  integer  false  "Items per page (default: 20, max: 100)"
// @Param        status     query  integer  false  "Filter by status (0=pending, 1=processing, 2=failed, 3=completed)"
// @Param        keyword    query  string   false  "Search by keywords"
// @Param        title      query  string   false  "Search by title or content"
// @Success      200  {object}  ListItemsDocResp
// @Router /console/api/v1/items [GET]
func ListItems(ctx context.Context, c *app.RequestContext) {
	page, pageSize := parsePagination(c)

	var statusFilter *int32
	if raw := strings.TrimSpace(c.Query("status")); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 32); err == nil {
			sv := int32(v)
			statusFilter = &sv
		}
	}
	keyword := strPtr(strings.TrimSpace(c.Query("keyword")))
	title := strPtr(strings.TrimSpace(c.Query("title")))

	items, total, err := dal.ListItems(db.DB, dal.ListItemsParams{
		Page:     page,
		PageSize: pageSize,
		Status:   statusFilter,
		Keyword:  keyword,
		Title:    title,
	})
	if err != nil {
		writeConsoleError(c, "database query failed: "+err.Error())
		return
	}

	itemInfos := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		itemInfos = append(itemInfos, toConsoleItemInfo(item))
	}

	c.JSON(consts.StatusOK, map[string]interface{}{
		"code": 0,
		"msg":  "success",
		"data": map[string]interface{}{
			"items":     itemInfos,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		},
	})
}

// ===========================================================================
// Handlers: Impr Records
// ===========================================================================

// ListAgentImprItems godoc
// @Summary      List agent impression records
// @Description  Returns Redis impr record and matched item details by agent_id
// @Tags         console
// @Produce      json
// @Param        agent_id  query  integer  true  "Agent ID"
// @Success      200  {object}  ListAgentImprItemsResp
// @Router /console/api/v1/impr/items [GET]
func ListAgentImprItems(ctx context.Context, c *app.RequestContext) {
	agentIDStr := strings.TrimSpace(c.Query("agent_id"))
	agentID, err := strconv.ParseInt(agentIDStr, 10, 64)
	if err != nil || agentID <= 0 {
		c.JSON(consts.StatusOK, &ListAgentImprItemsResp{Code: 1, Msg: "invalid agent_id"})
		return
	}

	record, err := dal.GetAgentImprRecord(ctx, agentID)
	if err != nil {
		c.JSON(consts.StatusOK, &ListAgentImprItemsResp{Code: 1, Msg: "query impr record failed: " + err.Error()})
		return
	}

	items := make([]map[string]interface{}, 0, len(record.Items))
	for _, item := range record.Items {
		items = append(items, toConsoleItemInfo(item))
	}

	itemIDStrings := make([]string, 0, len(record.ItemIDs))
	for _, id := range record.ItemIDs {
		itemIDStrings = append(itemIDStrings, strconv.FormatInt(id, 10))
	}

	groupIDStrings := make([]string, 0, len(record.GroupIDs))
	for _, id := range record.GroupIDs {
		groupIDStrings = append(groupIDStrings, strconv.FormatInt(id, 10))
	}

	c.JSON(consts.StatusOK, &ListAgentImprItemsResp{
		Code: 0, Msg: "success",
		Data: &ListAgentImprItemsData{
			AgentID: strconv.FormatInt(agentID, 10), ItemIDs: itemIDStrings,
			GroupIDs: groupIDStrings, URLs: record.URLs, Items: items,
		},
	})
}

// ===========================================================================
// Handlers: Milestone Rules
// ===========================================================================

// ListMilestoneRules godoc
// @Summary      List milestone rules
// @Description  Returns a paginated list of milestone rules with optional filters
// @Tags         console
// @Produce      json
// @Param        page          query  integer  false  "Page number (default: 1)"
// @Param        page_size     query  integer  false  "Items per page (default: 20, max: 100)"
// @Param        metric_key    query  string   false  "Filter by metric key"
// @Param        rule_enabled  query  boolean  false  "Filter by enabled status"
// @Success      200  {object}  ListMilestoneRulesResp
// @Router /console/api/v1/milestone-rules [GET]
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
		Page: page, PageSize: pageSize, MetricKey: metricKey, RuleEnabled: ruleEnabled,
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
		Code: 0, Msg: "success",
		Data: &ListMilestoneRulesData{Rules: respRules, Total: total, Page: page, PageSize: pageSize},
	})
}

// CreateMilestoneRule godoc
// @Summary      Create milestone rule
// @Tags         console
// @Accept       json
// @Produce      json
// @Param        body  body  createMilestoneRuleReq  true  "Create request"
// @Success      200  {object}  MilestoneRuleResp
// @Router /console/api/v1/milestone-rules [POST]
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
		MetricKey: strings.TrimSpace(req.MetricKey), Threshold: req.Threshold,
		RuleEnabled: ruleEnabled, ContentTemplate: req.ContentTemplate,
	})
	if err != nil {
		writeRuleMutationError(c, err)
		return
	}

	c.JSON(consts.StatusOK, &MilestoneRuleResp{
		Code: 0, Msg: "success", Data: &MilestoneRuleData{Rule: toMilestoneRuleInfo(*rule)},
	})
}

// UpdateMilestoneRule godoc
// @Summary      Update milestone rule
// @Tags         console
// @Accept       json
// @Produce      json
// @Param        rule_id  path  integer  true  "Rule ID"
// @Param        body     body  updateMilestoneRuleReq  true  "Update request"
// @Success      200  {object}  MilestoneRuleResp
// @Router /console/api/v1/milestone-rules/:rule_id [PUT]
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
		RuleEnabled: req.RuleEnabled, ContentTemplate: req.ContentTemplate,
	})
	if err != nil {
		writeRuleMutationError(c, err)
		return
	}

	c.JSON(consts.StatusOK, &MilestoneRuleResp{
		Code: 0, Msg: "success", Data: &MilestoneRuleData{Rule: toMilestoneRuleInfo(*rule)},
	})
}

// ReplaceMilestoneRule godoc
// @Summary      Replace milestone rule
// @Tags         console
// @Accept       json
// @Produce      json
// @Param        rule_id  path  integer  true  "Rule ID"
// @Param        body     body  replaceMilestoneRuleReq  true  "Replace request"
// @Success      200  {object}  ReplaceMilestoneRuleResp
// @Router /console/api/v1/milestone-rules/:rule_id/replace [POST]
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
		MetricKey: strings.TrimSpace(req.MetricKey), Threshold: req.Threshold,
		RuleEnabled: ruleEnabled, ContentTemplate: req.ContentTemplate,
	})
	if err != nil {
		writeRuleMutationError(c, err)
		return
	}

	c.JSON(consts.StatusOK, &ReplaceMilestoneRuleResp{
		Code: 0, Msg: "success",
		Data: &ReplaceMilestoneRuleData{OldRule: toMilestoneRuleInfo(*oldRule), NewRule: toMilestoneRuleInfo(*newRule)},
	})
}

// ===========================================================================
// Handlers: System Notifications
// ===========================================================================

// ListSystemNotifications godoc
// @Summary      List system notifications
// @Tags         console
// @Produce      json
// @Param        page       query  integer  false  "Page number (default: 1)"
// @Param        page_size  query  integer  false  "Items per page (default: 20, max: 100)"
// @Param        status     query  integer  false  "Filter by status (0=draft, 1=active, 2=offline)"
// @Success      200  {object}  ListSystemNotificationsResp
// @Router /console/api/v1/system-notifications [GET]
func ListSystemNotifications(ctx context.Context, c *app.RequestContext) {
	page, pageSize := parsePagination(c)

	var statusFilter *int32
	if raw := strings.TrimSpace(c.Query("status")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			writeConsoleError(c, "invalid status")
			return
		}
		v := int32(parsed)
		statusFilter = &v
	}

	rows, total, err := dal.ListSystemNotifications(db.DB, dal.ListSystemNotificationsParams{
		Page: page, PageSize: pageSize, Status: statusFilter,
	})
	if err != nil {
		writeConsoleError(c, "database query failed: "+err.Error())
		return
	}

	infos := make([]SystemNotificationInfo, 0, len(rows))
	for _, row := range rows {
		infos = append(infos, toSystemNotificationInfo(row))
	}

	c.JSON(consts.StatusOK, &ListSystemNotificationsResp{
		Code: 0, Msg: "success",
		Data: &ListSystemNotificationsData{Notifications: infos, Total: total, Page: page, PageSize: pageSize},
	})
}

// CreateSystemNotification godoc
// @Summary      Create system notification
// @Tags         console
// @Accept       json
// @Produce      json
// @Param        body  body  createSystemNotificationReq  true  "Create request"
// @Success      200  {object}  SystemNotificationResp
// @Router /console/api/v1/system-notifications [POST]
func CreateSystemNotification(ctx context.Context, c *app.RequestContext) {
	var req createSystemNotificationReq
	if err := c.BindAndValidate(&req); err != nil {
		writeConsoleError(c, "invalid request: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Type) == "" {
		writeConsoleError(c, "type is required")
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeConsoleError(c, "content is required")
		return
	}
	if req.StartAt != nil && req.EndAt != nil && *req.StartAt > 0 && *req.EndAt > 0 && *req.EndAt <= *req.StartAt {
		writeConsoleError(c, "end_at must be greater than start_at")
		return
	}

	if notifIDGen == nil {
		writeConsoleError(c, "notification id generator not initialized")
		return
	}
	notifID, err := notifIDGen.NextID()
	if err != nil {
		writeConsoleError(c, "generate notification id failed: "+err.Error())
		return
	}

	var status int16
	if req.Status != nil {
		status = int16(*req.Status)
	}
	var startAt, endAt int64
	if req.StartAt != nil {
		startAt = *req.StartAt
	}
	if req.EndAt != nil {
		endAt = *req.EndAt
	}

	row, err := dal.CreateSystemNotification(ctx, db.DB, dal.CreateSystemNotificationParams{
		NotificationID: notifID, Type: strings.TrimSpace(req.Type),
		Content: strings.TrimSpace(req.Content), Status: status, StartAt: startAt, EndAt: endAt,
	})
	if err != nil {
		writeConsoleError(c, "create failed: "+err.Error())
		return
	}

	if row.Status == model.StatusActive {
		svc := notifService()
		if putErr := svc.ActiveStore().Put(ctx, row); putErr != nil {
			writeConsoleError(c, "created but redis sync failed: "+putErr.Error())
			return
		}
	}

	c.JSON(consts.StatusOK, &SystemNotificationResp{
		Code: 0, Msg: "success",
		Data: &SystemNotificationData{Notification: toSystemNotificationInfo(*row)},
	})
}

// UpdateSystemNotification godoc
// @Summary      Update system notification
// @Tags         console
// @Accept       json
// @Produce      json
// @Param        notification_id  path  integer  true  "Notification ID"
// @Param        body  body  updateSystemNotificationReq  true  "Update request"
// @Success      200  {object}  SystemNotificationResp
// @Router /console/api/v1/system-notifications/:notification_id [PUT]
func UpdateSystemNotification(ctx context.Context, c *app.RequestContext) {
	notifID, ok := parseNotificationID(c)
	if !ok {
		return
	}

	var req updateSystemNotificationReq
	if err := c.BindAndValidate(&req); err != nil {
		writeConsoleError(c, "invalid request: "+err.Error())
		return
	}
	if req.Type == nil && req.Content == nil && req.Status == nil && req.StartAt == nil && req.EndAt == nil {
		writeConsoleError(c, "at least one field must be provided")
		return
	}

	row, err := dal.UpdateSystemNotification(ctx, db.DB, notifID, dal.UpdateSystemNotificationParams{
		Type: req.Type, Content: req.Content, Status: req.Status, StartAt: req.StartAt, EndAt: req.EndAt,
	})
	if err != nil {
		writeNotificationError(c, err)
		return
	}

	syncActiveStore(ctx, row)

	c.JSON(consts.StatusOK, &SystemNotificationResp{
		Code: 0, Msg: "success",
		Data: &SystemNotificationData{Notification: toSystemNotificationInfo(*row)},
	})
}

// OfflineSystemNotification godoc
// @Summary      Offline system notification
// @Tags         console
// @Produce      json
// @Param        notification_id  path  integer  true  "Notification ID"
// @Success      200  {object}  SystemNotificationResp
// @Router /console/api/v1/system-notifications/:notification_id/offline [POST]
func OfflineSystemNotification(ctx context.Context, c *app.RequestContext) {
	notifID, ok := parseNotificationID(c)
	if !ok {
		return
	}

	row, err := dal.OfflineSystemNotification(ctx, db.DB, notifID)
	if err != nil {
		writeNotificationError(c, err)
		return
	}

	svc := notifService()
	_ = svc.ActiveStore().Remove(ctx, notifID)

	c.JSON(consts.StatusOK, &SystemNotificationResp{
		Code: 0, Msg: "success",
		Data: &SystemNotificationData{Notification: toSystemNotificationInfo(*row)},
	})
}

// ===========================================================================
// Shared helpers
// ===========================================================================

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

func parseNotificationID(c *app.RequestContext) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("notification_id")), 10, 64)
	if err != nil || id <= 0 {
		writeConsoleError(c, "invalid notification_id")
		return 0, false
	}
	return id, true
}

func writeConsoleError(c *app.RequestContext, msg string) {
	c.JSON(consts.StatusOK, map[string]interface{}{"code": 1, "msg": msg})
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

func writeNotificationError(c *app.RequestContext, err error) {
	if errors.Is(err, dal.ErrNotificationNotFound) {
		writeConsoleError(c, err.Error())
		return
	}
	writeConsoleError(c, err.Error())
}

func syncActiveStore(ctx context.Context, row *model.SystemNotification) {
	svc := notifService()
	if row.Status == model.StatusActive && row.OfflineAt == 0 {
		_ = svc.ActiveStore().Put(ctx, row)
	} else {
		_ = svc.ActiveStore().Remove(ctx, row.NotificationID)
	}
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toConsoleItemInfo(item dal.ItemWithProcessed) map[string]interface{} {
	info := map[string]interface{}{
		"item_id":         strconv.FormatInt(item.ItemID, 10),
		"author_agent_id": strconv.FormatInt(item.AuthorAgentID, 10),
		"raw_content":     item.RawContent,
		"raw_notes":       item.RawNotes,
		"raw_url":         item.RawURL,
		"created_at":      item.CreatedAt,
	}
	if item.Status != nil {
		info["status"] = int32(*item.Status)
	}
	info["summary"] = item.Summary
	info["broadcast_type"] = item.BroadcastType
	if item.Domains != nil && *item.Domains != "" {
		info["domains"] = strings.Split(*item.Domains, ",")
	}
	if item.Keywords != nil && *item.Keywords != "" {
		info["keywords"] = strings.Split(*item.Keywords, ",")
	}
	info["expire_time"] = item.ExpireTime
	info["geo"] = item.Geo
	info["source_type"] = item.SourceType
	info["expected_response"] = item.ExpectedResponse
	if item.GroupID != nil && *item.GroupID != 0 {
		info["group_id"] = strconv.FormatInt(*item.GroupID, 10)
	}
	info["updated_at"] = item.UpdatedAt
	return info
}

func toMilestoneRuleInfo(rule model.MilestoneRule) MilestoneRuleInfo {
	return MilestoneRuleInfo{
		RuleID: strconv.FormatInt(rule.RuleID, 10), MetricKey: rule.MetricKey,
		Threshold: rule.Threshold, RuleEnabled: rule.RuleEnabled,
		ContentTemplate: rule.ContentTemplate, CreatedAt: rule.CreatedAt, UpdatedAt: rule.UpdatedAt,
	}
}

func toSystemNotificationInfo(n model.SystemNotification) SystemNotificationInfo {
	return SystemNotificationInfo{
		NotificationID: strconv.FormatInt(n.NotificationID, 10), Type: n.Type, Content: n.Content,
		Status: int32(n.Status), AudienceType: n.AudienceType, AudienceExpression: n.AudienceExpression,
		StartAt: n.StartAt, EndAt: n.EndAt, OfflineAt: n.OfflineAt, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt,
	}
}
