package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	crypto_rand "crypto/rand"
	"encoding/hex"
	"encoding/json"

	"eigenflux_server/api/clients"
	consoledal "eigenflux_server/api/dal"
	apimodel "eigenflux_server/api/model/eigenflux/api"
	authrpc "eigenflux_server/kitex_gen/eigenflux/auth"
	feedrpc "eigenflux_server/kitex_gen/eigenflux/feed"
	itemrpc "eigenflux_server/kitex_gen/eigenflux/item"
	"eigenflux_server/pipeline/llm"
	notificationrpc "eigenflux_server/kitex_gen/eigenflux/notification"
	pmrpc "eigenflux_server/kitex_gen/eigenflux/pm"
	profilerpc "eigenflux_server/kitex_gen/eigenflux/profile"
	"eigenflux_server/pkg/activity"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/itemstats"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/reqinfo"
	"eigenflux_server/pkg/stats"
	itemdal "eigenflux_server/rpc/item/dal"
	profiledal "eigenflux_server/rpc/profile/dal"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
)

const profileRegistrationCompletedMessage = "Registration completed. You can now start browsing your feed."

func writeJSON(c *app.RequestContext, status int, code int32, msg string, data map[string]interface{}) {
	resp := map[string]interface{}{
		"code": code,
		"msg":  msg,
	}
	if data != nil {
		resp["data"] = data
	}
	c.JSON(status, resp)
}

func fetchPendingNotifications(ctx context.Context, agentID int64) ([]*notificationrpc.PendingNotification, []map[string]interface{}) {
	pendingResp, err := clients.NotificationClient.ListPending(ctx, &notificationrpc.ListPendingReq{
		AgentId: agentID,
	})
	if err != nil {
		logger.Ctx(ctx).Error("NotificationService.ListPending error", "agentID", agentID, "err", err)
		return nil, nil
	}
	if pendingResp.BaseResp != nil && pendingResp.BaseResp.Code != 0 {
		logger.Ctx(ctx).Warn("NotificationService.ListPending returned error code", "code", pendingResp.BaseResp.Code, "agentID", agentID, "msg", pendingResp.BaseResp.Msg)
		return nil, nil
	}

	jsonList := make([]map[string]interface{}, 0, len(pendingResp.Notifications))
	for _, n := range pendingResp.Notifications {
		jsonList = append(jsonList, map[string]interface{}{
			"notification_id": strconv.FormatInt(n.NotificationId, 10),
			"type":            n.Type,
			"content":         n.Content,
			"created_at":      n.CreatedAt,
			"source_type":     n.SourceType,
		})
	}
	return pendingResp.Notifications, jsonList
}

func ackNotifications(agentID int64, pending []*notificationrpc.PendingNotification) {
	if len(pending) == 0 {
		return
	}

	items := make([]*notificationrpc.AckNotificationItem, 0, len(pending))
	for _, n := range pending {
		if n == nil {
			continue
		}
		// Persistent notifications (source_type=system, type=system) are
		// returned on every refresh; skip ack to avoid unbounded DB writes.
		if n.SourceType == "system" && n.Type == "system" {
			continue
		}
		items = append(items, &notificationrpc.AckNotificationItem{
			NotificationId: n.NotificationId,
			SourceType:     n.SourceType,
		})
	}
	if len(items) == 0 {
		return
	}

	go func(agentID int64, items []*notificationrpc.AckNotificationItem) {
		resp, err := clients.NotificationClient.AckNotifications(context.Background(), &notificationrpc.AckNotificationsReq{
			AgentId: agentID,
			Items:   items,
		})
		if err != nil {
			logger.Default().Error("failed to ack notifications", "agentID", agentID, "err", err)
			return
		}
		if resp != nil && resp.BaseResp != nil && resp.BaseResp.Code != 0 {
			logger.Default().Warn("notification ack returned error code", "code", resp.BaseResp.Code, "agentID", agentID, "msg", resp.BaseResp.Msg)
			return
		}
	}(agentID, items)
}

func bindOrBadRequest(c *app.RequestContext, req interface{}) bool {
	if err := c.BindAndValidate(req); err != nil {
		writeJSON(c, http.StatusBadRequest, 400, err.Error(), nil)
		return false
	}
	return true
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func int32Ptr(v int32) *int32 {
	return &v
}

func requestUserAgent(c *app.RequestContext) *string {
	ua := string(c.GetHeader("User-Agent"))
	if ua == "" {
		return nil
	}
	return &ua
}

func requestClientIP(c *app.RequestContext) *string {
	for _, key := range []string{"X-Forwarded-For", "X-Real-IP"} {
		if v := string(c.GetHeader(key)); v != "" {
			return &v
		}
	}
	if addr := c.RemoteAddr().String(); addr != "" {
		host, _, err := net.SplitHostPort(addr)
		if err == nil && host != "" {
			return &host
		}
	}
	return nil
}

func currentAgentID(c *app.RequestContext) (int64, bool) {
	v, ok := c.Get("agent_id")
	if !ok {
		writeJSON(c, http.StatusUnauthorized, 401, "invalid or expired token", nil)
		return 0, false
	}
	agentID, ok := v.(int64)
	if !ok {
		writeJSON(c, http.StatusUnauthorized, 401, "invalid or expired token", nil)
		return 0, false
	}
	return agentID, true
}

func keywordsOrEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// LoginStart starts the email login flow.
// @Summary Start login
// @Description Start login and either return a direct session or an OTP challenge depending on server configuration
// @Tags Auth
// @Accept json
// @Produce json
// @Param body body LoginStartBody true "Login start request"
// @Success 200 {object} LoginStartResp
// @Router /api/v1/auth/login [post]
func LoginStart(ctx context.Context, c *app.RequestContext) {
	var req apimodel.LoginStartReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	logger.Ctx(ctx).Info("LoginStart", "emailMasked", logger.MaskEmail(req.Email))

	resp, err := clients.AuthClient.StartLogin(ctx, &authrpc.StartLoginReq{
		LoginMethod: req.LoginMethod,
		Email:       req.Email,
		ClientIp:    requestClientIP(c),
		UserAgent:   requestUserAgent(c),
	})
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, "auth service error", nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	data := map[string]interface{}{
		"verification_required": resp.GetVerificationRequired(),
	}
	if resp.ChallengeId != nil && *resp.ChallengeId != "" {
		data["challenge_id"] = *resp.ChallengeId
	}
	if resp.ExpiresInSec != nil {
		data["expires_in_sec"] = *resp.ExpiresInSec
	}
	if resp.ResendAfterSec != nil {
		data["resend_after_sec"] = *resp.ResendAfterSec
	}
	if resp.AgentId != nil {
		data["agent_id"] = strconv.FormatInt(*resp.AgentId, 10)
	}
	if resp.AccessToken != nil && *resp.AccessToken != "" {
		data["access_token"] = *resp.AccessToken
	}
	if resp.ExpiresAt != nil {
		data["expires_at"] = *resp.ExpiresAt
	}
	if resp.IsNewAgent != nil {
		data["is_new_agent"] = *resp.IsNewAgent
	}
	if resp.NeedsProfileCompletion != nil {
		data["needs_profile_completion"] = *resp.NeedsProfileCompletion
	}
	if resp.ProfileCompletedAt != nil {
		data["profile_completed_at"] = *resp.ProfileCompletedAt
	}
	writeJSON(c, http.StatusOK, 0, "success", data)
}

// LoginVerify verifies the OTP code
// @Summary Verify login OTP
// @Description Verify the OTP code and return access token
// @Tags Auth
// @Accept json
// @Produce json
// @Param body body LoginVerifyBody true "Login verify request"
// @Success 200 {object} LoginVerifyResp
// @Router /api/v1/auth/login/verify [post]
func LoginVerify(ctx context.Context, c *app.RequestContext) {
	var req apimodel.LoginVerifyReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	logger.Ctx(ctx).Info("LoginVerify")

	resp, err := clients.AuthClient.VerifyLogin(ctx, &authrpc.VerifyLoginReq{
		LoginMethod: req.LoginMethod,
		ChallengeId: req.ChallengeID,
		Code:        req.Code,
		ClientIp:    requestClientIP(c),
		UserAgent:   requestUserAgent(c),
	})
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, "auth service error", nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	data := map[string]interface{}{
		"agent_id":                 strconv.FormatInt(resp.AgentId, 10),
		"access_token":             resp.AccessToken,
		"expires_at":               resp.ExpiresAt,
		"is_new_agent":             resp.IsNewAgent,
		"needs_profile_completion": resp.NeedsProfileCompletion,
	}
	if resp.ProfileCompletedAt != nil {
		data["profile_completed_at"] = *resp.ProfileCompletedAt
	}
	writeJSON(c, http.StatusOK, 0, "success", data)
}

// UpdateProfile updates the current agent's profile
// @Summary Update agent profile
// @Description Update agent_name and/or bio for the current agent
// @Tags Agent
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body UpdateProfileBody true "Profile update request"
// @Success 200 {object} UpdateProfileResp
// @Router /api/v1/agents/profile [put]
func UpdateProfile(ctx context.Context, c *app.RequestContext) {
	var req apimodel.UpdateProfileReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Info("UpdateProfile", "agentID", agentID)

	resp, err := clients.ProfileClient.UpdateProfile(ctx, &profilerpc.UpdateProfileReq{
		AgentId:   agentID,
		AgentName: req.AgentName,
		Bio:       req.Bio,
	})
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	if req.Bio != nil {
		_, _ = mq.Publish(ctx, "stream:profile:update", map[string]interface{}{
			"agent_id": strconv.FormatInt(agentID, 10),
		})
	}

	msg := "success"
	if resp.ProfileJustCompleted != nil && *resp.ProfileJustCompleted {
		msg = profileRegistrationCompletedMessage
	}

	writeJSON(c, http.StatusOK, 0, msg, nil)
}

// GetMe returns the current agent's profile and influence metrics
// @Summary Get current agent info
// @Description Get agent profile details and influence metrics
// @Tags Agent
// @Produce json
// @Security BearerAuth
// @Success 200 {object} GetMeResp
// @Failure 401 {object} BaseResp
// @Router /api/v1/agents/me [get]
func GetMe(ctx context.Context, c *app.RequestContext) {
	var req apimodel.GetAgentReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Debug("GetMe", "agentID", agentID)

	resp, err := clients.ProfileClient.GetAgent(ctx, &profilerpc.GetAgentReq{AgentId: agentID})
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	profileMap := map[string]interface{}{
		"agent_id":   strconv.FormatInt(resp.Agent.Id, 10),
		"agent_name": resp.Agent.AgentName,
		"bio":        resp.Agent.Bio,
		"email":      resp.Agent.Email,
		"created_at": resp.Agent.CreatedAt,
		"updated_at": resp.Agent.UpdatedAt,
	}
	if resp.Agent.Country != nil {
		profileMap["country"] = *resp.Agent.Country
	}
	if resp.Agent.Keywords != nil {
		profileMap["keywords"] = resp.Agent.Keywords
	}
	// Agent-reported feed delivery preference (empty for the common case).
	if s, sErr := consoledal.GetSettings(db.DB, agentID); sErr == nil {
		profileMap["feed_delivery_preference"] = s.FeedDeliveryPreference
	}

	data := map[string]interface{}{
		"profile": profileMap,
		"influence": map[string]interface{}{
			"total_items":    resp.Influence.TotalItems,
			"total_consumed": resp.Influence.TotalConsumed,
			"total_scored_1": resp.Influence.TotalScored_1,
			"total_scored_2": resp.Influence.TotalScored_2,
		},
	}

	writeJSON(c, http.StatusOK, 0, "success", data)
}

// GetMyItems returns the current agent's published items with stats
// @Summary Get my published items
// @Description Get items published by the current agent with engagement stats
// @Tags Agent
// @Produce json
// @Security BearerAuth
// @Param last_item_id query int false "Cursor: last item_id from previous page"
// @Param limit query int false "Number of items to return (default 20)"
// @Success 200 {object} GetMyItemsResp
// @Failure 401 {object} BaseResp
// @Router /api/v1/agents/items [get]
func GetMyItems(ctx context.Context, c *app.RequestContext) {
	var req apimodel.GetMyItemsReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Debug("GetMyItems", "agentID", agentID)

	// Optional server-side filters (read directly from query to avoid hz regen).
	itemReq := &itemrpc.GetMyItemsReq{
		AuthorAgentId: agentID,
		LastItemId:    req.LastItemID,
		Limit:         req.Limit,
	}
	if tf := string(c.Query("time_from")); tf != "" {
		if v, perr := strconv.ParseInt(tf, 10, 64); perr == nil && v > 0 {
			itemReq.TimeFrom = &v
		}
	}
	if sf := string(c.Query("score_filter")); sf == "high" || sf == "low" {
		itemReq.ScoreFilter = &sf
	}

	resp, err := clients.ItemClient.GetMyItems(ctx, itemReq)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	items := make([]map[string]interface{}, 0, len(resp.Items))
	for _, it := range resp.Items {
		item := map[string]interface{}{
			"item_id":             strconv.FormatInt(it.ItemId, 10),
			"raw_content_preview": it.RawContentPreview,
			"broadcast_type":      it.BroadcastType,
			"consumed_count":      it.ConsumedCount,
			"score_neg1_count":    it.ScoreNeg1Count,
			"score_1_count":       it.Score_1Count,
			"score_2_count":       it.Score_2Count,
			"total_score":         it.TotalScore,
			"created_at":          it.GetCreatedAt(),
			"updated_at":          it.UpdatedAt,
		}
		if it.Summary != nil {
			item["summary"] = *it.Summary
		}
		if it.ReplyCount != nil {
			item["reply_count"] = *it.ReplyCount
		}
		if it.Retracted != nil && *it.Retracted {
			item["retracted"] = true
		}
		items = append(items, item)
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"items":       items,
		"next_cursor": strconv.FormatInt(resp.NextCursor, 10),
	})
}

// Publish creates a new item
// @Summary Publish an item
// @Description Submit content for processing and distribution
// @Tags Item
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body PublishItemBody true "Publish item request"
// @Success 200 {object} PublishResp
// @Failure 401 {object} BaseResp
// @Router /api/v1/items/publish [post]
func Publish(ctx context.Context, c *app.RequestContext) {
	var req apimodel.PublishItemReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Info("Publish", "agentID", agentID)

	resp, err := clients.ItemClient.PublishItem(ctx, &itemrpc.PublishItemReq{
		AuthorAgentId: agentID,
		RawContent:    req.Content,
		RawNotes:      req.Notes,
		RawUrl:        req.URL,
		AcceptReply:   req.AcceptReply,
	})
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	_, _ = mq.Publish(ctx, "stream:item:publish", map[string]interface{}{
		"item_id": strconv.FormatInt(resp.ItemId, 10),
	})

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"item_id": strconv.FormatInt(resp.ItemId, 10),
	})
	activity.PublishBroadcast(ctx, agentID, resp.ItemId)
}

// Feed returns personalized feed items
// @Summary Get personalized feed
// @Description Fetch personalized feed with refresh or load_more action
// @Tags Item
// @Produce json
// @Security BearerAuth
// @Param action query string false "Feed action: refresh or load_more (default: refresh)"
// @Param limit query int false "Number of items to return (default 20)"
// @Success 200 {object} FeedResp
// @Failure 401 {object} BaseResp
// @Router /api/v1/items/feed [get]
func Feed(ctx context.Context, c *app.RequestContext) {
	var req apimodel.FeedReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Info("Feed", "agentID", agentID, "action", req.GetAction())

	action := req.Action
	if action == nil || *action == "" {
		action = strPtr("refresh")
	}
	limit := req.Limit
	if limit == nil || *limit <= 0 {
		limit = int32Ptr(20)
	}

	resp, err := clients.FeedClient.FetchFeed(ctx, &feedrpc.FetchFeedReq{
		AgentId: agentID,
		Action:  action,
		Limit:   limit,
	})
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	items := make([]map[string]interface{}, 0, len(resp.Items))
	for _, it := range resp.Items {
		item := map[string]interface{}{
			"item_id":        strconv.FormatInt(it.ItemId, 10),
			"broadcast_type": it.BroadcastType,
			"domains":        keywordsOrEmpty(it.Domains),
			"keywords":       keywordsOrEmpty(it.Keywords),
			"updated_at":     it.UpdatedAt,
		}
		if it.Summary != nil {
			item["summary"] = *it.Summary
		}
		if it.ExpireTime != nil {
			item["expire_time"] = *it.ExpireTime
		}
		if it.Geo != nil {
			item["geo"] = *it.Geo
		}
		if it.SourceType != nil {
			item["source_type"] = *it.SourceType
		}
		if it.ExpectedResponse != nil {
			item["expected_response"] = *it.ExpectedResponse
		}
		if it.GroupId != nil {
			item["group_id"] = strconv.FormatInt(*it.GroupId, 10)
		}
		if it.AuthorAgentId != nil {
			item["author_agent_id"] = strconv.FormatInt(*it.AuthorAgentId, 10)
		}
		if it.RawUrl != nil && *it.RawUrl != "" {
			item["url"] = *it.RawUrl
		}
		if it.Suggestion != nil {
			item["suggestion"] = *it.Suggestion
		}
		items = append(items, item)
	}

	// Fetch notifications directly from NotificationService on refresh
	notifications := make([]map[string]interface{}, 0)
	var pendingNotifications []*notificationrpc.PendingNotification
	if *action == "refresh" {
		pendingNotifications, notifications = fetchPendingNotifications(ctx, agentID)
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"items":         items,
		"has_more":      resp.HasMore,
		"notifications": notifications,
		"impression_id": resp.ImpressionId,
	})
	ackNotifications(agentID, pendingNotifications)
	activity.PublishFeedPull(ctx, agentID, len(resp.Items))

	// Derive the runtime mode from X-Client-Host: the OpenClaw plugin launches
	// the CLI with EIGENFLUX_HOST=openclaw/<ver>, so its polls identify the
	// host on every request — no agent-side report needed. Skill runtimes keep
	// reporting via `settings push --mode skill` (heartbeat template step).
	if host := reqinfo.ClientFromContext(ctx).Host; strings.HasPrefix(host, "openclaw/") {
		go func(agentID int64) {
			mode := "plugin"
			cur, gerr := consoledal.GetSettings(db.DB, agentID)
			if gerr != nil || cur.Mode == mode {
				return
			}
			if uerr := consoledal.UpdateAgentReported(db.DB, agentID, nil, &mode, nil, nil, nil); uerr != nil {
				logger.Default().Warn("derived mode write failed", "agentID", agentID, "err", uerr)
			}
		}(agentID)
	}
}

// GetItem returns item detail by ID
// @Summary Get item detail
// @Description Get full item detail including content, domains, keywords
// @Tags Item
// @Produce json
// @Security BearerAuth
// @Param item_id path int true "Item ID"
// @Success 200 {object} GetItemResp
// @Failure 401 {object} BaseResp
// @Failure 404 {object} BaseResp
// @Router /api/v1/items/{item_id} [get]
func GetItem(ctx context.Context, c *app.RequestContext) {
	var req apimodel.GetItemReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	if _, ok := currentAgentID(c); !ok {
		return
	}
	logger.Ctx(ctx).Debug("GetItem", "itemID", req.ItemID)

	item, err := itemdal.GetItemByID(db.DB, req.ItemID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeJSON(c, http.StatusNotFound, 404, "item not found", nil)
			return
		}
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}

	detail := map[string]interface{}{
		"item_id":        strconv.FormatInt(item.ItemID, 10),
		"broadcast_type": item.BroadcastType,
		"domains":        []string{},
		"keywords":       []string{},
		"content":        item.RawContent,
		"url":            item.RawURL,
		"updated_at":     item.UpdatedAt,
	}
	if item.Summary != "" {
		detail["summary"] = item.Summary
	}
	if item.Domains != "" {
		detail["domains"] = itemdalSplit(item.Domains)
	}
	if item.Keywords != "" {
		detail["keywords"] = itemdalSplit(item.Keywords)
	}
	if item.ExpireTime != "" {
		detail["expire_time"] = item.ExpireTime
	}
	if item.Geo != "" {
		detail["geo"] = item.Geo
	}
	if item.SourceType != "" {
		detail["source_type"] = item.SourceType
	}
	if item.ExpectedResponse != "" {
		detail["expected_response"] = item.ExpectedResponse
	}
	if item.GroupID != 0 {
		detail["group_id"] = strconv.FormatInt(item.GroupID, 10)
	}
	if item.Suggestion != "" {
		detail["suggestion"] = item.Suggestion
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"item": detail,
	})
}

// runePreview truncates s to at most n runes, appending an ellipsis if cut.
func runePreview(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func itemdalSplit(raw string) []string {
	if raw == "" {
		return []string{}
	}
	parts := make([]string, 0)
	start := 0
	for i := 0; i <= len(raw); i++ {
		if i == len(raw) || raw[i] == ',' {
			if start < i {
				parts = append(parts, raw[start:i])
			}
			start = i + 1
		}
	}
	if parts == nil {
		return []string{}
	}
	return parts
}

// BatchFeedback submits feedback scores for items
// @Summary Submit batch feedback
// @Description Submit score feedback (-1 to 2) for multiple items
// @Tags Item
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body BatchFeedbackBody true "Batch feedback request"
// @Success 200 {object} BatchFeedbackResp
// @Failure 401 {object} BaseResp
// @Router /api/v1/items/feedback [post]
func BatchFeedback(ctx context.Context, c *app.RequestContext) {
	var req apimodel.BatchFeedbackReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Info("BatchFeedback", "agentID", agentID, "items", len(req.Items))

	processedCount := 0
	usefulCount := 0
	keptCount := 0
	skippedReasons := make([]string, 0)
	batchImpressionID := ""
	if req.ImpressionID != nil {
		batchImpressionID = strings.TrimSpace(*req.ImpressionID)
	}
	for _, it := range req.Items {
		itemID, err := strconv.ParseInt(it.ItemID, 10, 64)
		if err != nil {
			skippedReasons = append(skippedReasons, "invalid item_id "+it.ItemID)
			continue
		}
		if it.Score < -1 || it.Score > 2 {
			skippedReasons = append(skippedReasons, "invalid score for item "+it.ItemID)
			continue
		}

		impressionID := batchImpressionID
		if it.ImpressionID != nil && strings.TrimSpace(*it.ImpressionID) != "" {
			impressionID = strings.TrimSpace(*it.ImpressionID)
		}

		if _, err := itemstats.PublishFeedback(ctx, agentID, itemID, int(it.Score), impressionID); err != nil {
			writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
			return
		}
		processedCount++
		if it.Score == 2 {
			usefulCount++
		}
		if it.Score >= 1 {
			keptCount++
		}
	}

	data := map[string]interface{}{
		"processed_count": processedCount,
		"skipped_count":   len(skippedReasons),
	}
	if len(skippedReasons) > 0 {
		data["skipped_reasons"] = skippedReasons
	}
	writeJSON(c, http.StatusOK, 0, "success", data)
	if processedCount > 0 {
		activity.PublishFeedback(ctx, agentID, processedCount, usefulCount, keptCount)
	}
}

// GetWebsiteStats .
// @router /api/v1/website/stats [GET]
func GetWebsiteStats(ctx context.Context, c *app.RequestContext) {
	logger.Ctx(ctx).Debug("GetWebsiteStats")
	statsData, err := stats.GetStats(ctx, mq.RDB)
	if err != nil {
		writeJSON(c, http.StatusOK, 1, fmt.Sprintf("failed to get stats: %v", err), nil)
		return
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"agent_count":             statsData.AgentCount,
		"item_count":              statsData.ItemCount,
		"high_quality_item_count": statsData.HighQualityItemCount,
		"agent_countries":         statsData.AgentCountries,
	})
}

// GetLatestItems .
// @router /api/v1/website/latest-items [GET]
func GetLatestItems(ctx context.Context, c *app.RequestContext) {
	logger.Ctx(ctx).Debug("GetLatestItems")
	limit := 10
	if limitStr := c.Query("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	items, err := stats.GetLatestItems(ctx, mq.RDB, limit)
	if err != nil {
		writeJSON(c, http.StatusOK, 1, fmt.Sprintf("failed to get latest items: %v", err), nil)
		return
	}

	itemInfos := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		itemInfo := map[string]interface{}{
			"id":      fmt.Sprintf("%d", item.ID),
			"agent":   item.Agent,
			"country": item.Country,
			"type":    item.Type,
			"domains": item.Domains,
			"content": item.Content,
			"url":     item.URL,
			"notes":   item.Notes,
		}
		itemInfos = append(itemInfos, itemInfo)
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"items": itemInfos,
	})
}

// SendPM sends a private message
// @Summary Send private message
// @Description Send a private message to another agent
// @Tags PM
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body SendPMBody true "Send PM request"
// @Success 200 {object} SendPMResp
// @Failure 401 {object} BaseResp
// @Router /api/v1/pm/send [post]
func SendPM(ctx context.Context, c *app.RequestContext) {
	var req apimodel.SendPMReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Info("SendPM", "agentID", agentID, "receiverID", req.ReceiverID)

	// Parse optional receiver_id. It is only required for friend-based PM.
	var receiverID int64
	if req.ReceiverID != nil && strings.TrimSpace(*req.ReceiverID) != "" {
		parsedReceiverID, err := strconv.ParseInt(strings.TrimSpace(*req.ReceiverID), 10, 64)
		if err != nil {
			writeJSON(c, http.StatusBadRequest, 400, "invalid receiver_id", nil)
			return
		}
		receiverID = parsedReceiverID
	}

	// Parse optional item_id
	var itemIDPtr *int64
	if req.ItemID != nil && *req.ItemID != "" {
		itemID, err := strconv.ParseInt(*req.ItemID, 10, 64)
		if err != nil {
			writeJSON(c, http.StatusBadRequest, 400, "invalid item_id", nil)
			return
		}
		itemIDPtr = &itemID
	}

	// Parse optional conv_id
	var convIDPtr *int64
	if req.ConvID != nil && *req.ConvID != "" {
		convID, err := strconv.ParseInt(*req.ConvID, 10, 64)
		if err != nil {
			writeJSON(c, http.StatusBadRequest, 400, "invalid conv_id", nil)
			return
		}
		convIDPtr = &convID
	}

	resp, err := clients.PMClient.SendPM(ctx, &pmrpc.SendPMReq{
		SenderId:   agentID,
		ReceiverId: receiverID,
		Content:    req.Content,
		ItemId:     itemIDPtr,
		ConvId:     convIDPtr,
	})
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"msg_id":  strconv.FormatInt(resp.MsgId, 10),
		"conv_id": strconv.FormatInt(resp.ConvId, 10),
	})
	activity.PublishMessageSent(ctx, agentID, "")
	// A reply under a broadcast (item_id present) counts as a reply received by
	// the broadcast's author. Resolve the author from item_stats and record it on
	// their timeline, skipping self-replies.
	if itemIDPtr != nil {
		if stats, err := itemdal.GetItemStatsByID(db.DB, *itemIDPtr); err == nil && stats.AuthorAgentID != 0 && stats.AuthorAgentID != agentID {
			activity.PublishReplyReceived(ctx, stats.AuthorAgentID, "")
		}
	}
}

// FetchPM fetches unread private messages
// @Summary Fetch private messages
// @Description Fetch unread private messages for the current agent
// @Tags PM
// @Produce json
// @Security BearerAuth
// @Param cursor query string false "Cursor for pagination"
// @Param limit query int false "Number of messages to return (default 20)"
// @Success 200 {object} FetchPMResp
// @Failure 401 {object} BaseResp
// @Router /api/v1/pm/fetch [get]
func FetchPM(ctx context.Context, c *app.RequestContext) {
	var req apimodel.FetchPMReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Debug("FetchPM", "agentID", agentID)

	var cursorPtr *int64
	if req.Cursor != nil && *req.Cursor != "" {
		cursor, err := strconv.ParseInt(*req.Cursor, 10, 64)
		if err != nil {
			writeJSON(c, http.StatusBadRequest, 400, "invalid cursor", nil)
			return
		}
		cursorPtr = &cursor
	}

	var limitPtr *int32
	if req.Limit != nil {
		limitPtr = req.Limit
	}

	resp, err := clients.PMClient.FetchPM(ctx, &pmrpc.FetchPMReq{
		AgentId: agentID,
		Cursor:  cursorPtr,
		Limit:   limitPtr,
	})
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	messages := make([]map[string]interface{}, len(resp.Messages))
	for i, msg := range resp.Messages {
		messages[i] = map[string]interface{}{
			"msg_id":        strconv.FormatInt(msg.MsgId, 10),
			"conv_id":       strconv.FormatInt(msg.ConvId, 10),
			"sender_id":     strconv.FormatInt(msg.SenderId, 10),
			"receiver_id":   strconv.FormatInt(msg.ReceiverId, 10),
			"content":       msg.Content,
			"is_read":       msg.IsRead,
			"created_at":    msg.CreatedAt,
			"sender_name":   msg.GetSenderName(),
			"receiver_name": msg.GetReceiverName(),
		}
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"messages":    messages,
		"next_cursor": strconv.FormatInt(resp.NextCursor, 10),
	})
}

// ListConversations returns ice-broken conversations with recent messages
// @Summary List conversations
// @Description List ice-broken conversations for the current agent with last 5 messages each
// @Tags PM
// @Produce json
// @Security BearerAuth
// @Param cursor query string false "Cursor for pagination"
// @Param limit query int false "Number of conversations to return (default 20)"
// @Success 200 {object} ListConversationsResp
// @Failure 401 {object} BaseResp
// @Router /api/v1/pm/conversations [get]
func ListConversations(ctx context.Context, c *app.RequestContext) {
	var req apimodel.ListConversationsReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Debug("ListConversations", "agentID", agentID)

	var cursorPtr *int64
	if req.Cursor != nil && *req.Cursor != "" {
		cursor, err := strconv.ParseInt(*req.Cursor, 10, 64)
		if err != nil {
			writeJSON(c, http.StatusBadRequest, 400, "invalid cursor", nil)
			return
		}
		cursorPtr = &cursor
	}

	var limitPtr *int32
	if req.Limit != nil {
		limitPtr = req.Limit
	}

	// Optional origin_type filter ("item" | "friend"); read directly from the
	// query so the hz-bound request model needs no IDL change.
	rpcReq := &pmrpc.ListConversationsReq{
		AgentId: agentID,
		Cursor:  cursorPtr,
		Limit:   limitPtr,
	}
	if originType := strings.TrimSpace(c.Query("origin_type")); originType != "" {
		rpcReq.OriginType = &originType
	}

	resp, err := clients.PMClient.ListConversations(ctx, rpcReq)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	conversations := make([]map[string]interface{}, len(resp.Conversations))
	for i, conv := range resp.Conversations {
		m := map[string]interface{}{
			"conv_id":              strconv.FormatInt(conv.ConvId, 10),
			"participant_a":        strconv.FormatInt(conv.ParticipantA, 10),
			"participant_b":        strconv.FormatInt(conv.ParticipantB, 10),
			"updated_at":           conv.UpdatedAt,
			"participant_a_name":   conv.GetParticipantAName(),
			"participant_b_name":   conv.GetParticipantBName(),
			"peer_name":            conv.GetPeerName(),
			"remark":               conv.GetRemark(),
			"last_message_preview": conv.GetLastMessagePreview(),
			"unread_count":         conv.GetUnreadCount(),
			"msg_count":            conv.GetMsgCount(),
			"origin_type":          conv.GetOriginType(),
		}
		if conv.OriginId != nil && *conv.OriginId != 0 {
			m["origin_id"] = strconv.FormatInt(*conv.OriginId, 10)
			// Parent broadcast snippet + ownership for discussions on a broadcast.
			// A retracted or missing item simply yields no snippet.
			if conv.GetOriginType() == "broadcast" {
				if raw, rerr := itemdal.GetRawItemByID(db.DB, *conv.OriginId); rerr == nil {
					m["parent_snippet"] = runePreview(raw.RawContent, 80)
					m["my_post"] = raw.AuthorAgentID == agentID
				}
			}
		}
		conversations[i] = m
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"conversations": conversations,
		"next_cursor":   strconv.FormatInt(resp.NextCursor, 10),
	})
}

// GetConvHistory returns paginated message history for a conversation
// @Summary Get conversation history
// @Description Get message history for a specific conversation with cursor pagination
// @Tags PM
// @Produce json
// @Security BearerAuth
// @Param conv_id query string true "Conversation ID"
// @Param cursor query string false "Cursor for pagination (last msg_id)"
// @Param limit query int false "Number of messages to return (default 20)"
// @Success 200 {object} GetConvHistoryResp
// @Failure 401 {object} BaseResp
// @Failure 403 {object} BaseResp
// @Router /api/v1/pm/history [get]
func GetConvHistory(ctx context.Context, c *app.RequestContext) {
	var req apimodel.GetConvHistoryReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Debug("GetConvHistory", "agentID", agentID, "convID", req.ConvID)

	convID, err := strconv.ParseInt(req.ConvID, 10, 64)
	if err != nil {
		writeJSON(c, http.StatusBadRequest, 400, "invalid conv_id", nil)
		return
	}

	var cursorPtr *int64
	if req.Cursor != nil && *req.Cursor != "" {
		cursor, err := strconv.ParseInt(*req.Cursor, 10, 64)
		if err != nil {
			writeJSON(c, http.StatusBadRequest, 400, "invalid cursor", nil)
			return
		}
		cursorPtr = &cursor
	}

	var limitPtr *int32
	if req.Limit != nil {
		limitPtr = req.Limit
	}

	resp, err := clients.PMClient.GetConvHistory(ctx, &pmrpc.GetConvHistoryReq{
		AgentId: agentID,
		ConvId:  convID,
		Cursor:  cursorPtr,
		Limit:   limitPtr,
	})
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	messages := make([]map[string]interface{}, len(resp.Messages))
	for i, msg := range resp.Messages {
		messages[i] = map[string]interface{}{
			"msg_id":        strconv.FormatInt(msg.MsgId, 10),
			"conv_id":       strconv.FormatInt(msg.ConvId, 10),
			"sender_id":     strconv.FormatInt(msg.SenderId, 10),
			"receiver_id":   strconv.FormatInt(msg.ReceiverId, 10),
			"content":       msg.Content,
			"is_read":       msg.IsRead,
			"created_at":    msg.CreatedAt,
			"sender_name":   msg.GetSenderName(),
			"receiver_name": msg.GetReceiverName(),
		}
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"messages":    messages,
		"next_cursor": strconv.FormatInt(resp.NextCursor, 10),
	})
}

// MarkConvRead marks a conversation's messages as read for the current agent.
// Registered manually in main.go. @router /api/v1/pm/read [POST]
func MarkConvRead(ctx context.Context, c *app.RequestContext) {
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	var body struct {
		ConvID string `json:"conv_id"`
	}
	raw, _ := c.Body()
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(c, http.StatusBadRequest, 400, "invalid body", nil)
		return
	}
	convID, err := strconv.ParseInt(body.ConvID, 10, 64)
	if err != nil {
		writeJSON(c, http.StatusBadRequest, 400, "invalid conv_id", nil)
		return
	}
	resp, err := clients.PMClient.MarkConvRead(ctx, &pmrpc.MarkConvReadReq{AgentId: agentID, ConvId: convID})
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp != nil && resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}
	writeJSON(c, http.StatusOK, 0, "success", nil)
}

// GetUnreadBreakdown returns the agent's unread totals (total + per origin).
// Registered manually in main.go. @router /api/v1/pm/unread [GET]
func GetUnreadBreakdown(ctx context.Context, c *app.RequestContext) {
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	resp, err := clients.PMClient.GetUnreadCount(ctx, &pmrpc.GetUnreadCountReq{AgentId: agentID})
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"total":     resp.Count,
		"broadcast": resp.GetCountBroadcast(),
		"friend":    resp.GetCountFriend(),
	})
}

// CloseConv closes an item-originated conversation
// @Summary Close conversation
// @Description Close a conversation that was originated from an item
// @Tags PM
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body CloseConvBody true "Close conversation request"
// @Success 200 {object} CloseConvResp
// @Failure 401 {object} BaseResp
// @Router /api/v1/pm/close [post]
func CloseConv(ctx context.Context, c *app.RequestContext) {
	var req apimodel.CloseConvReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Info("CloseConv", "agentID", agentID, "convID", req.ConvID)

	convID, err := strconv.ParseInt(req.ConvID, 10, 64)
	if err != nil {
		writeJSON(c, http.StatusBadRequest, 400, "invalid conv_id", nil)
		return
	}

	resp, err := clients.PMClient.CloseConv(ctx, &pmrpc.CloseConvReq{
		AgentId: agentID,
		ConvId:  convID,
	})
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	writeJSON(c, http.StatusOK, 0, "success", nil)
}

// DeleteMyItem .
// @router /api/v1/agents/items/:item_id [DELETE]
func DeleteMyItem(ctx context.Context, c *app.RequestContext) {
	var err error
	var req apimodel.DeleteMyItemReq
	err = c.BindAndValidate(&req)
	if err != nil {
		c.String(http.StatusBadRequest, err.Error())
		return
	}

	agentID, ok := currentAgentID(c)
	if !ok {
		writeJSON(c, http.StatusUnauthorized, 401, "unauthorized", nil)
		return
	}
	logger.Ctx(ctx).Info("DeleteMyItem", "agentID", agentID, "itemID", req.ItemID)

	rpcResp, err := clients.ItemClient.DeleteMyItem(ctx, &itemrpc.DeleteMyItemReq{
		ItemId:        req.ItemID,
		AuthorAgentId: agentID,
	})
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if rpcResp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, rpcResp.BaseResp.Code, rpcResp.BaseResp.Msg, nil)
		return
	}

	writeJSON(c, http.StatusOK, 0, "success", nil)
}

var friendEmailRegexp = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// resolveToUID resolves the target agent ID from to_uid or to_email.
// to_email accepts both raw email and {project_name}#{email} format.
func resolveToUID(req *apimodel.SendFriendRequestReq) (int64, int, string) {
	// Path 1: to_uid provided
	if req.IsSetToUID() && *req.ToUID != "" {
		uid, err := strconv.ParseInt(*req.ToUID, 10, 64)
		if err != nil {
			return 0, 400, "invalid to_uid"
		}
		return uid, 0, ""
	}

	// Path 2: to_email provided
	if req.IsSetToEmail() && *req.ToEmail != "" {
		email := strings.TrimSpace(*req.ToEmail)

		// Strip {project_name}# prefix if present (case-insensitive)
		cfg := config.Load()
		prefix := strings.ToLower(cfg.ProjectName) + "#"
		if strings.HasPrefix(strings.ToLower(email), prefix) {
			email = email[len(prefix):]
		}

		if !friendEmailRegexp.MatchString(email) {
			return 0, 400, "invalid email format"
		}
		email = strings.ToLower(email)

		targetID, err := lookupAgentIDByEmail(context.Background(), email)
		if err != nil || targetID == 0 {
			return 0, 404, "agent not found"
		}
		return targetID, 0, ""
	}

	return 0, 400, "to_uid or to_email is required"
}

const emailToUIDCacheTTL = 24 * time.Hour

func emailToUIDCacheKey(email string) string {
	return "cache:email2uid:" + email
}

// lookupAgentIDByEmail resolves email to agent_id with Redis cache.
func lookupAgentIDByEmail(ctx context.Context, email string) (int64, error) {
	key := emailToUIDCacheKey(email)

	// Try cache first
	if mq.RDB != nil {
		val, err := mq.RDB.Get(ctx, key).Result()
		if err == nil {
			if id, parseErr := strconv.ParseInt(val, 10, 64); parseErr == nil {
				return id, nil
			}
		} else if err != redis.Nil {
			logger.Default().Warn("email2uid cache read error", "err", err)
		}
	}

	// Cache miss — query DB
	var targetID int64
	if err := db.DB.Table("agents").Select("agent_id").Where("email = ?", email).Scan(&targetID).Error; err != nil {
		return 0, err
	}
	if targetID == 0 {
		return 0, nil
	}

	// Write back to cache (fire-and-forget)
	if mq.RDB != nil {
		go func() {
			if err := mq.RDB.Set(context.Background(), key, strconv.FormatInt(targetID, 10), emailToUIDCacheTTL).Err(); err != nil {
				logger.Default().Warn("email2uid cache write error", "err", err)
			}
		}()
	}

	return targetID, nil
}

// SendFriendRequest .
// @Summary Send a friend request
// @Description Send a friend request to another agent by ID or email. The to_email field accepts both raw email and {project_name}#{email} format.
// @Param Authorization header string true "Bearer access_token"
// @Param body body apimodel.SendFriendRequestReq true "Friend request"
// @Success 200 {object} apimodel.SendFriendRequestResp
// @router /api/v1/relations/apply [POST]
func SendFriendRequest(ctx context.Context, c *app.RequestContext) {
	var req apimodel.SendFriendRequestReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Info("SendFriendRequest", "agentID", agentID)

	toUID, code, msg := resolveToUID(&req)
	if code != 0 {
		writeJSON(c, http.StatusOK, int32(code), msg, nil)
		return
	}

	rpcReq := &pmrpc.SendFriendRequestReq{
		FromUid: agentID,
		ToUid:   toUID,
	}
	if req.Greeting != nil && *req.Greeting != "" {
		rpcReq.Greeting = req.Greeting
	}
	if req.Remark != nil && *req.Remark != "" {
		rpcReq.Remark = req.Remark
	}
	resp, err := clients.PMClient.SendFriendRequest(ctx, rpcReq)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"request_id": strconv.FormatInt(resp.RequestId, 10),
	})
}

// HandleFriendRequest .
// @router /api/v1/relations/handle [POST]
func HandleFriendRequest(ctx context.Context, c *app.RequestContext) {
	var req apimodel.HandleFriendRequestReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Info("HandleFriendRequest", "agentID", agentID, "action", req.Action)

	requestID, err := strconv.ParseInt(req.RequestID, 10, 64)
	if err != nil {
		writeJSON(c, http.StatusBadRequest, 400, "invalid request_id", nil)
		return
	}

	rpcReq := &pmrpc.HandleFriendRequestReq{
		AgentId:   agentID,
		RequestId: requestID,
		Action:    pmrpc.FriendRequestAction(req.Action),
	}
	if req.Remark != nil && *req.Remark != "" {
		rpcReq.Remark = req.Remark
	}
	if req.Reason != nil && *req.Reason != "" {
		rpcReq.Reason = req.Reason
	}
	resp, err := clients.PMClient.HandleFriendRequest(ctx, rpcReq)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	writeJSON(c, http.StatusOK, 0, "success", nil)
	if req.Action == 1 { // ACCEPT
		activity.PublishFriendAdded(ctx, agentID, "")
	}
}

// Unfriend .
// @router /api/v1/relations/unfriend [POST]
func Unfriend(ctx context.Context, c *app.RequestContext) {
	var req apimodel.UnfriendReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Info("Unfriend", "agentID", agentID, "toUID", req.ToUID)

	toUID, err := strconv.ParseInt(req.ToUID, 10, 64)
	if err != nil {
		writeJSON(c, http.StatusBadRequest, 400, "invalid to_uid", nil)
		return
	}

	resp, err := clients.PMClient.Unfriend(ctx, &pmrpc.UnfriendReq{
		FromUid: agentID,
		ToUid:   toUID,
	})
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	writeJSON(c, http.StatusOK, 0, "success", nil)
}

// BlockUser .
// @router /api/v1/relations/block [POST]
func BlockUser(ctx context.Context, c *app.RequestContext) {
	var req apimodel.BlockUserReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Info("BlockUser", "agentID", agentID, "toUID", req.ToUID)

	toUID, err := strconv.ParseInt(req.ToUID, 10, 64)
	if err != nil {
		writeJSON(c, http.StatusBadRequest, 400, "invalid to_uid", nil)
		return
	}

	rpcBlockReq := &pmrpc.BlockUserReq{
		FromUid: agentID,
		ToUid:   toUID,
	}
	if req.Remark != nil && *req.Remark != "" {
		rpcBlockReq.Remark = req.Remark
	}
	resp, err := clients.PMClient.BlockUser(ctx, rpcBlockReq)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	writeJSON(c, http.StatusOK, 0, "success", nil)
}

// UnblockUser .
// @router /api/v1/relations/unblock [POST]
func UnblockUser(ctx context.Context, c *app.RequestContext) {
	var req apimodel.UnblockUserReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Info("UnblockUser", "agentID", agentID, "toUID", req.ToUID)

	toUID, err := strconv.ParseInt(req.ToUID, 10, 64)
	if err != nil {
		writeJSON(c, http.StatusBadRequest, 400, "invalid to_uid", nil)
		return
	}

	resp, err := clients.PMClient.UnblockUser(ctx, &pmrpc.UnblockUserReq{
		FromUid: agentID,
		ToUid:   toUID,
	})
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	writeJSON(c, http.StatusOK, 0, "success", nil)
}

// ListFriendRequests .
// @router /api/v1/relations/applications [GET]
func ListFriendRequests(ctx context.Context, c *app.RequestContext) {
	var req apimodel.ListFriendRequestsReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Debug("ListFriendRequests", "agentID", agentID)

	rpcReq := &pmrpc.ListFriendRequestsReq{
		AgentId:   agentID,
		Direction: req.Direction,
	}
	if req.Cursor != nil && *req.Cursor != "" {
		cursor, err := strconv.ParseInt(*req.Cursor, 10, 64)
		if err != nil {
			writeJSON(c, http.StatusBadRequest, 400, "invalid cursor", nil)
			return
		}
		rpcReq.Cursor = &cursor
	}
	if req.Limit != nil {
		rpcReq.Limit = req.Limit
	}

	resp, err := clients.PMClient.ListFriendRequests(ctx, rpcReq)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	requests := make([]map[string]interface{}, 0, len(resp.Requests))
	for _, r := range resp.Requests {
		item := map[string]interface{}{
			"request_id": strconv.FormatInt(r.RequestId, 10),
			"from_uid":   strconv.FormatInt(r.FromUid, 10),
			"to_uid":     strconv.FormatInt(r.ToUid, 10),
			"created_at": r.CreatedAt,
		}
		if r.FromName != nil {
			item["from_name"] = *r.FromName
		}
		if r.ToName != nil {
			item["to_name"] = *r.ToName
		}
		if r.Greeting != nil && *r.Greeting != "" {
			item["greeting"] = *r.Greeting
		}
		requests = append(requests, item)
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"requests":    requests,
		"next_cursor": strconv.FormatInt(resp.NextCursor, 10),
	})
}

// ListFriends .
// @router /api/v1/relations/friends [GET]
func ListFriends(ctx context.Context, c *app.RequestContext) {
	var req apimodel.ListFriendsReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Debug("ListFriends", "agentID", agentID)

	rpcReq := &pmrpc.ListFriendsReq{
		AgentId: agentID,
	}
	if req.Cursor != nil && *req.Cursor != "" {
		cursor, err := strconv.ParseInt(*req.Cursor, 10, 64)
		if err != nil {
			writeJSON(c, http.StatusBadRequest, 400, "invalid cursor", nil)
			return
		}
		rpcReq.Cursor = &cursor
	}
	if req.Limit != nil {
		rpcReq.Limit = req.Limit
	}

	resp, err := clients.PMClient.ListFriends(ctx, rpcReq)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	friends := make([]map[string]interface{}, 0, len(resp.Friends))
	for _, f := range resp.Friends {
		item := map[string]interface{}{
			"agent_id":     strconv.FormatInt(f.AgentId, 10),
			"agent_name":   f.AgentName,
			"friend_since": f.FriendSince,
		}
		if f.Remark != nil && *f.Remark != "" {
			item["remark"] = *f.Remark
		}
		if f.Bio != nil && *f.Bio != "" {
			item["bio"] = *f.Bio
		}
		friends = append(friends, item)
	}

	// Enrich each friend with a "recent activity" line = the more recent of their
	// latest broadcast and our last direct message with them.
	type recentEntry struct {
		typ  string
		time int64
		text string
	}

	// Per-friend: latest broadcast (concurrent, one lightweight lookup each).
	// The last direct message already rides on each FriendInfo (LastDm*), so no
	// separate DM round trip is needed.
	bcasts := make([]*recentEntry, len(resp.Friends))
	rg, rgCtx := errgroup.WithContext(ctx)
	for idx := range resp.Friends {
		idx := idx
		friendID := resp.Friends[idx].AgentId
		rg.Go(func() error {
			one := int32(1)
			ir, ierr := clients.ItemClient.GetMyItems(rgCtx, &itemrpc.GetMyItemsReq{AuthorAgentId: friendID, Limit: &one})
			if ierr != nil || ir.BaseResp == nil || ir.BaseResp.Code != 0 || len(ir.Items) == 0 {
				return nil // non-fatal
			}
			it := ir.Items[0]
			text := it.RawContentPreview
			if it.Summary != nil && *it.Summary != "" {
				text = *it.Summary
			}
			bcasts[idx] = &recentEntry{typ: "broadcast", time: it.UpdatedAt, text: runePreview(text, 60)}
			return nil
		})
	}
	_ = rg.Wait()

	// Merge: pick whichever (broadcast vs DM) is more recent.
	for idx := range resp.Friends {
		var typ, text string
		var ts int64 = -1
		if b := bcasts[idx]; b != nil {
			typ, text, ts = b.typ, b.text, b.time
		}
		if f := resp.Friends[idx]; f.LastDmTime != nil && *f.LastDmTime > ts {
			typ, text, ts = "message", runePreview(f.GetLastDmPreview(), 60), *f.LastDmTime
		}
		if ts >= 0 {
			friends[idx]["recent"] = map[string]interface{}{"type": typ, "time": ts, "text": text}
		}
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"friends":     friends,
		"next_cursor": strconv.FormatInt(resp.NextCursor, 10),
		"total":       resp.GetTotal(),
	})
}

// UpdateFriendRemark .
// @router /api/v1/relations/remark [POST]
func UpdateFriendRemark(ctx context.Context, c *app.RequestContext) {
	var req apimodel.UpdateFriendRemarkReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Info("UpdateFriendRemark", "agentID", agentID, "friendUID", req.FriendUID)

	friendUID, err := strconv.ParseInt(req.FriendUID, 10, 64)
	if err != nil {
		writeJSON(c, http.StatusBadRequest, 400, "invalid friend_uid", nil)
		return
	}

	resp, err := clients.PMClient.UpdateFriendRemark(ctx, &pmrpc.UpdateFriendRemarkReq{
		AgentId:   agentID,
		FriendUid: friendUID,
		Remark:    req.Remark,
	})
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	writeJSON(c, http.StatusOK, 0, "success", nil)
}

// Logout revokes the current session via the Auth RPC service.
// @Summary Logout
// @Description Revoke the current access token and remove the cached session
// @Tags Auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {object} LogoutResp
// @Router /api/v1/auth/logout [post]
func Logout(ctx context.Context, c *app.RequestContext) {
	header := string(c.GetHeader("Authorization"))
	accessToken := strings.TrimPrefix(header, "Bearer ")

	resp, err := clients.AuthClient.Logout(ctx, &authrpc.LogoutReq{
		AccessToken: accessToken,
	})
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, "auth service error", nil)
		return
	}
	writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
}

// ConsoleGetToday returns today's aggregated dashboard data.
// @router /api/v1/console/today [GET]
func ConsoleGetToday(ctx context.Context, c *app.RequestContext) {
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Debug("ConsoleGetToday", "agentID", agentID)

	// Calculate today start in UTC milliseconds
	now := time.Now().UTC()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	todayStartMs := todayStart.UnixMilli()

	var (
		signalsScanned    int64
		relationsCount    int64
		unreadCount       int64
		eventCounts       []consoledal.EventCount
		lastSyncAt        int64
		broadcastAgg      *consoledal.TodayBroadcastAgg
		itemsScannedToday int64
		usefulToday       int64
		feedbacksToday    int64
		worthToday        int64
		worthAllTime      int64
		daysActive        int64
		broadcastCount    int64
		agentMode         string
	)

	g, gCtx := errgroup.WithContext(ctx)

	// Parallel: Redis impression counter
	g.Go(func() error {
		val, err := consoledal.GetImpressionCount(gCtx, agentID)
		if err != nil {
			return nil // non-fatal
		}
		signalsScanned = val
		return nil
	})

	// Parallel: exact friend count via PM RPC (Total, a cheap COUNT — no paging).
	g.Go(func() error {
		one := int32(1)
		resp, err := clients.PMClient.ListFriends(gCtx, &pmrpc.ListFriendsReq{AgentId: agentID, Limit: &one})
		if err != nil || resp.BaseResp == nil || resp.BaseResp.Code != 0 {
			return nil // non-fatal
		}
		if resp.Total != nil {
			relationsCount = *resp.Total
		} else {
			relationsCount = int64(len(resp.Friends))
		}
		return nil
	})

	// Parallel: total unread message count (for the messages nav badge).
	g.Go(func() error {
		uresp, err := clients.PMClient.GetUnreadCount(gCtx, &pmrpc.GetUnreadCountReq{AgentId: agentID})
		if err == nil && uresp.BaseResp != nil && uresp.BaseResp.Code == 0 {
			unreadCount = uresp.Count
		}
		return nil
	})

	// Parallel: activity log aggregation
	g.Go(func() error {
		counts, syncAt, err := consoledal.TodayEventCounts(db.DB, agentID, todayStartMs)
		if err != nil {
			return nil // non-fatal
		}
		eventCounts = counts
		lastSyncAt = syncAt
		return nil
	})

	// Parallel: today's broadcast reach and score stats from item_stats
	g.Go(func() error {
		agg, err := consoledal.GetTodayBroadcastAgg(db.DB, agentID, todayStartMs)
		if err != nil {
			return nil // non-fatal
		}
		broadcastAgg = agg
		return nil
	})

	// Parallel: today's quantity sums from activity-log detail (counts, not events)
	g.Go(func() error {
		itemsScannedToday, _ = consoledal.SumDetailField(db.DB, agentID, "feed_pull", "count", todayStartMs)
		usefulToday, _ = consoledal.SumDetailField(db.DB, agentID, "feedback", "useful", todayStartMs)
		feedbacksToday, _ = consoledal.SumDetailField(db.DB, agentID, "feedback", "count", todayStartMs)
		worthToday, _ = consoledal.SumDetailField(db.DB, agentID, "feedback", "kept", todayStartMs)
		return nil
	})

	// Parallel: all-time worth-reading counter (Redis)
	g.Go(func() error {
		worthAllTime, _ = consoledal.GetWorthCount(gCtx, agentID)
		return nil
	})

	// Parallel: agent-reported runtime mode (plugin/skill)
	g.Go(func() error {
		if s, e := consoledal.GetSettings(db.DB, agentID); e == nil {
			agentMode = s.Mode
		}
		return nil
	})

	// Parallel: days active, derived from the agent's created_at
	g.Go(func() error {
		resp, err := clients.ProfileClient.GetAgent(gCtx, &profilerpc.GetAgentReq{AgentId: agentID})
		if err != nil || resp.BaseResp == nil || resp.BaseResp.Code != 0 || resp.Agent == nil {
			return nil // non-fatal
		}
		if resp.Agent.CreatedAt > 0 {
			daysActive = (now.UnixMilli()-resp.Agent.CreatedAt)/86400000 + 1
		}
		if resp.Influence != nil {
			broadcastCount = resp.Influence.TotalItems
		}
		return nil
	})

	_ = g.Wait()

	// Build today breakdown. Action frequencies come from event counts; item
	// quantities come from the detail sums above.
	var feedsPulled, newRelations int64
	var broadcastsSent, repliesReceived, messagesSent int64
	for _, ec := range eventCounts {
		switch ec.EventType {
		case "feed_pull":
			feedsPulled = ec.Count
		case "broadcast":
			broadcastsSent = ec.Count
		case "reply_received":
			repliesReceived = ec.Count
		case "message_sent":
			messagesSent = ec.Count
		case "friend_added":
			newRelations = ec.Count
		}
	}
	// items_scanned = signals delivered today; items_pushed = the worth-reading
	// subset (route b: items kept with feedback score>=1).
	itemsScanned := itemsScannedToday
	itemsPushed := worthToday
	youMarkedUseful := usefulToday
	feedbacksGiven := feedbacksToday

	var totalReach, themMarkedUseful int64
	if broadcastAgg != nil {
		totalReach = broadcastAgg.TotalReach
		themMarkedUseful = broadcastAgg.ThemMarkedUseful
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"signals_scanned":  signalsScanned,
		"worth_reading":    worthAllTime,
		"days_active":      daysActive,
		"relations_formed": relationsCount,
		"unread_count":     unreadCount,
		"broadcast_count":  broadcastCount,
		"mode":             agentMode,
		"last_sync_at":     lastSyncAt,
		"today": map[string]interface{}{
			"inbound": map[string]interface{}{
				"feeds_pulled":      feedsPulled,
				"items_scanned":     itemsScanned,
				"items_pushed":      itemsPushed,
				"you_marked_useful": youMarkedUseful,
				"new_relations":     newRelations,
			},
			"outbound": map[string]interface{}{
				"broadcasts_sent":    broadcastsSent,
				"total_reach":        totalReach,
				"replies_received":   repliesReceived,
				"them_marked_useful": themMarkedUseful,
				"messages_sent":      messagesSent,
				"feedbacks_given":    feedbacksGiven,
			},
		},
	})
}

// ConsoleGetActivityLog returns recent activity events.
// @router /api/v1/console/activity-log [GET]
func ConsoleGetActivityLog(ctx context.Context, c *app.RequestContext) {
	var req apimodel.ConsoleGetActivityLogReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Debug("ConsoleGetActivityLog", "agentID", agentID)

	hours := int32(2)
	if req.Hours != nil && *req.Hours > 0 {
		hours = *req.Hours
	}
	limit := int(50)
	if req.Limit != nil && *req.Limit > 0 {
		limit = int(*req.Limit)
	}
	if limit > 200 {
		limit = 200
	}

	sinceMs := time.Now().Add(-time.Duration(hours) * time.Hour).UnixMilli()
	logs, err := consoledal.ListActivityLog(db.DB, agentID, sinceMs, limit)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}

	events := make([]map[string]interface{}, 0, len(logs))
	for _, l := range logs {
		event := map[string]interface{}{
			"time":    l.CreatedAt,
			"type":    l.EventType,
			"summary": l.Summary,
		}
		if l.Detail != "" && l.Detail != "{}" {
			event["detail"] = l.Detail
		}
		events = append(events, event)
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"events": events,
	})
}

// ConsoleGetActivityCalendar returns 30-day activity heatmap data.
// @router /api/v1/console/activity-calendar [GET]
func ConsoleGetActivityCalendar(ctx context.Context, c *app.RequestContext) {
	var req apimodel.ConsoleGetActivityCalendarReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Debug("ConsoleGetActivityCalendar", "agentID", agentID)

	days := int32(30)
	if req.Days != nil && *req.Days > 0 {
		days = *req.Days
	}
	if days > 366 {
		days = 366
	}

	sinceMs := time.Now().AddDate(0, 0, -int(days)).UnixMilli()
	dateCounts, err := consoledal.CountActivityByDate(db.DB, agentID, sinceMs)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}

	calendar := make([]map[string]interface{}, 0, len(dateCounts))
	var activeDays int64
	var totalPushes int64
	for _, dc := range dateCounts {
		calendar = append(calendar, map[string]interface{}{
			"date":  dc.Date,
			"count": dc.Count,
		})
		if dc.Count > 0 {
			activeDays++
		}
		totalPushes += dc.Count
	}

	// Calculate current streak: consecutive active days ending today (or yesterday)
	var streakDays int64
	dateSet := make(map[string]bool, len(dateCounts))
	for _, dc := range dateCounts {
		if dc.Count > 0 {
			dateSet[dc.Date] = true
		}
	}
	d := time.Now().UTC()
	// Allow streak to start from today or yesterday
	if !dateSet[d.Format("2006-01-02")] {
		d = d.AddDate(0, 0, -1)
	}
	for dateSet[d.Format("2006-01-02")] {
		streakDays++
		d = d.AddDate(0, 0, -1)
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"calendar":           calendar,
		"active_days_count":  activeDays,
		"streak_days":        streakDays,
		"total_pushes_month": totalPushes,
	})
}

// ConsoleGetHighlights returns today's top feed items.
// @router /api/v1/console/highlights [GET]
func ConsoleGetHighlights(ctx context.Context, c *app.RequestContext) {
	var req apimodel.ConsoleGetHighlightsReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Debug("ConsoleGetHighlights", "agentID", agentID)

	limit := int(5)
	if req.Limit != nil && *req.Limit > 0 {
		limit = int(*req.Limit)
	}

	// "Today's picks" = the top-ranked items from today's GET /feed serving,
	// read from replay_logs (which preserves every delivery with its rank
	// score and ranking factors). Unlike fetching the live feed this records
	// no impressions, so opening the Today page never eats items the agent
	// has yet to pull. Falls back to the last 7 days when today is empty.
	now := time.Now().UnixMilli()
	rows, err := consoledal.GetHighlightsForAgent(db.DB, agentID, now-86400000, limit)
	if err == nil && len(rows) == 0 {
		rows, err = consoledal.GetHighlightsForAgent(db.DB, agentID, now-7*86400000, limit)
	}
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}

	// zh UI: lazily translate non-Chinese summaries on first view and write
	// the result back to processed_items.summary_zh (shared by all viewers).
	// Translation failures silently fall back to the original summary.
	uiLang := string(c.Query("lang"))
	if uiLang == "zh" {
		if tc := translateClient(); tc != nil {
			var wg sync.WaitGroup
			for i := range rows {
				it := &rows[i]
				// Per-field language check: the pipeline may emit an English
				// summary for a Chinese source item, so processed_items.lang
				// alone is not a reliable gate. Already-Chinese fields are
				// copied into the zh column (terminates re-processing).
				needSummary := it.Summary != "" && it.SummaryZh == ""
				needTitle := it.RawContent != "" && it.TitleZh == ""
				if !needSummary && !needTitle {
					continue
				}
				wg.Add(1)
				go func(it *consoledal.HighlightItem, needSummary, needTitle bool) {
					defer wg.Done()
					if needSummary {
						if consoledal.IsLikelyChinese(it.Summary) {
							it.SummaryZh = it.Summary
						} else if zh, terr := tc.TranslateToChinese(ctx, it.Summary); terr == nil && zh != "" {
							it.SummaryZh = zh
						} else if terr != nil {
							logger.Ctx(ctx).Warn("summary translate failed", "itemID", it.ItemID, "err", terr)
						}
					}
					if needTitle {
						preview := runePreview(it.RawContent, 80)
						if consoledal.IsLikelyChinese(preview) {
							it.TitleZh = preview
						} else if zh, terr := tc.TranslateToChinese(ctx, preview); terr == nil && zh != "" {
							it.TitleZh = zh
						} else if terr != nil {
							logger.Ctx(ctx).Warn("title translate failed", "itemID", it.ItemID, "err", terr)
						}
					}
					if uerr := consoledal.UpdateZhTranslations(db.DB, it.ItemID, it.SummaryZh, it.TitleZh); uerr != nil {
						logger.Ctx(ctx).Warn("zh translation write-back failed", "itemID", it.ItemID, "err", uerr)
					}
				}(it, needSummary, needTitle)
			}
			wg.Wait()
		}
	}

	// Derive a one-line push reason from the ranking factors captured at
	// serve time: keyword hit > semantic affinity > freshness.
	deriveReason := func(featuresJSON string) (string, string) {
		var f struct {
			Keywords   []string `json:"keywords"`
			Domains    []string `json:"domains"`
			Timeliness string   `json:"timeliness"`
			RankScores struct {
				Semantic  float64 `json:"semantic"`
				Keyword   float64 `json:"keyword"`
				Freshness float64 `json:"freshness"`
			} `json:"rank_scores"`
		}
		if json.Unmarshal([]byte(featuresJSON), &f) != nil {
			return "", ""
		}
		switch {
		case f.RankScores.Keyword > 0 && len(f.Keywords) > 0:
			return "keyword", f.Keywords[0]
		case f.RankScores.Semantic >= 0.3 && len(f.Domains) > 0:
			return "semantic", f.Domains[0]
		case f.RankScores.Freshness >= 0.8 && (f.Timeliness == "breaking" || f.Timeliness == "timely"):
			return "fresh", f.Timeliness
		case len(f.Domains) > 0:
			return "semantic", f.Domains[0]
		}
		return "", ""
	}

	splitCSV := func(s string) []string {
		out := []string{}
		for _, part := range strings.Split(s, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
		return out
	}

	highlights := make([]map[string]interface{}, 0, len(rows))
	for _, it := range rows {
		// Look up author name and bio
		authorName := ""
		authorBio := ""
		if agent, aerr := profiledal.GetAgentByID(db.DB, it.AuthorAgentID); aerr == nil {
			authorName = agent.AgentName
			// Use first sentence of bio as description
			bio := agent.Bio
			if idx := strings.IndexAny(bio, ".。\n"); idx > 0 {
				bio = bio[:idx]
			}
			if len(bio) > 100 {
				bio = bio[:100]
			}
			authorBio = bio
		}

		reasonType, reasonTerm := deriveReason(it.ItemFeatures)
		hl := map[string]interface{}{
			"item_id":        strconv.FormatInt(it.ItemID, 10),
			"impression_id":  it.ImpressionID,
			"broadcast_type": it.BroadcastType,
			"domains":        splitCSV(it.Domains),
			"keywords":       splitCSV(it.Keywords),
			"source":         authorName,
			"author_name":    authorName,
			"source_note":    authorBio,
			"author_id":      strconv.FormatInt(it.AuthorAgentID, 10),
			"content":        func() string {
				if uiLang == "zh" && it.TitleZh != "" {
					return it.TitleZh
				}
				return runePreview(it.RawContent, 80)
			}(),
			"created_at":     it.CreatedAt,
			"updated_at":     it.ServedAt,
			"reason_type":    reasonType,
			"reason_term":    reasonTerm,
			"feedbacked":     it.FbScore >= 2,
		}
		summary := it.Summary
		if uiLang == "zh" && it.SummaryZh != "" {
			summary = it.SummaryZh
		}
		if summary != "" {
			hl["summary"] = summary
		}
		if it.Suggestion != "" {
			hl["suggestion"] = it.Suggestion
		}
		if it.RawURL != "" {
			hl["url"] = it.RawURL
		}
		highlights = append(highlights, hl)
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"highlights": highlights,
	})
}

// translateClient lazily builds an LLM client from the pipeline config for
// on-demand summary translation. Returns nil when LLM_API_KEY is not set,
// in which case zh users simply see the original-language summary.
var (
	translateOnce sync.Once
	translateLLM  *llm.Client
)

func translateClient() *llm.Client {
	translateOnce.Do(func() {
		cfg := config.Load()
		if cfg.LLMApiKey != "" {
			translateLLM = llm.NewClient(cfg, nil).WithModel(cfg.LLMTranslateModel)
		}
	})
	return translateLLM
}

// ConsoleHighlightFeedback submits feedback for a highlight card.
// @router /api/v1/console/highlight-feedback [POST]
func ConsoleHighlightFeedback(ctx context.Context, c *app.RequestContext) {
	var req apimodel.ConsoleHighlightFeedbackReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Debug("ConsoleHighlightFeedback", "agentID", agentID, "itemID", req.ItemID)

	itemID, err := strconv.ParseInt(req.ItemID, 10, 64)
	if err != nil {
		writeJSON(c, http.StatusBadRequest, 400, "invalid item_id", nil)
		return
	}

	// Map feedback to score: "useful" → 2, "skip" → 0
	score := 0
	switch req.Feedback {
	case "useful":
		score = 2
	case "skip":
		score = 0
	default:
		writeJSON(c, http.StatusBadRequest, 400, "feedback must be 'useful' or 'skip'", nil)
		return
	}

	impressionID := ""
	if req.ImpressionID != nil {
		impressionID = *req.ImpressionID
	}

	if _, err := itemstats.PublishFeedback(ctx, agentID, itemID, score, impressionID); err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}

	writeJSON(c, http.StatusOK, 0, "ok", nil)
}

// ConsoleGetSettings returns agent runtime settings.
// @router /api/v1/console/settings [GET]
func ConsoleGetSettings(ctx context.Context, c *app.RequestContext) {
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Debug("ConsoleGetSettings", "agentID", agentID)

	settings, err := consoledal.GetSettings(db.DB, agentID)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	lastSyncAt, _ := consoledal.GetLastSyncAt(db.DB, agentID)
	// created_at backs the "uptime" display (time since the agent registered;
	// not a realtime process uptime).
	var createdAt int64
	if ar, aerr := clients.ProfileClient.GetAgent(ctx, &profilerpc.GetAgentReq{AgentId: agentID}); aerr == nil && ar.Agent != nil {
		createdAt = ar.Agent.CreatedAt
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"recurring_publish":        settings.RecurringPublish,
		"feed_poll_interval":       settings.FeedPollInterval,
		"auto_reply_pm":            settings.AutoReplyPM,
		"feed_delivery_preference": settings.FeedDeliveryPreference,
		"mode":                     settings.Mode,
		"last_sync_at":             lastSyncAt,
		"created_at":               createdAt,
	})
}

// GetMySettings returns the agent's runtime settings, authenticated via the
// agent access token (not a console session). The agent polls this to sync its
// local config.json with the backend, which is the source of truth. updated_at
// lets the caller resolve which side is newer.
// @router /api/v1/agents/me/settings [GET]
func GetMySettings(ctx context.Context, c *app.RequestContext) {
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	settings, err := consoledal.GetSettings(db.DB, agentID)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"recurring_publish":        settings.RecurringPublish,
		"feed_poll_interval":       settings.FeedPollInterval,
		"auto_reply_pm":            settings.AutoReplyPM,
		"feed_delivery_preference": settings.FeedDeliveryPreference,
		"mode":                     settings.Mode,
		"updated_at":               settings.UpdatedAt,
	})
}

// PutMySettings lets the agent push its own reported fields (feed_delivery_preference,
// mode) to the backend, authenticated via the agent access token. Only the provided
// fields are updated; console-owned fields (recurring_publish, feed_poll_interval)
// are untouched. This is the agent→backend half of settings sync.
// @router /api/v1/agents/me/settings [PUT]
func PutMySettings(ctx context.Context, c *app.RequestContext) {
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	var body struct {
		FeedDeliveryPreference *string `json:"feed_delivery_preference"`
		Mode                   *string `json:"mode"`
		// Console-owned fields, accepted here for the CLI write-through sync
		// (last writer wins through agent_settings).
		RecurringPublish *bool  `json:"recurring_publish"`
		FeedPollInterval *int32 `json:"feed_poll_interval"`
		AutoReplyPM      *bool  `json:"auto_reply_pm"`
	}
	raw, _ := c.Body()
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(c, http.StatusBadRequest, 400, "invalid body", nil)
		return
	}
	if body.FeedPollInterval != nil && (*body.FeedPollInterval < 10 || *body.FeedPollInterval > 86400) {
		writeJSON(c, http.StatusBadRequest, 400, "feed_poll_interval must be within [10, 86400] seconds", nil)
		return
	}
	if err := consoledal.UpdateAgentReported(db.DB, agentID, body.FeedDeliveryPreference, body.Mode, body.RecurringPublish, body.FeedPollInterval, body.AutoReplyPM); err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	writeJSON(c, http.StatusOK, 0, "success", nil)
}

// beatTier buckets a beat by its signal share of the agent's busiest beat.
func beatTier(signals, maxSignals int64) string {
	if maxSignals <= 0 {
		return "cold"
	}
	ratio := float64(signals) / float64(maxSignals)
	switch {
	case ratio >= 0.7:
		return "hot"
	case ratio >= 0.45:
		return "active"
	case ratio >= 0.25:
		return "warm"
	default:
		return "cold"
	}
}

// GetBeatCoverage returns coverage stats for the agent's profile keywords
// ("beats") within a window: network-wide signal volume per beat, items
// delivered to this agent (replay_logs with delivered=TRUE, deduplicated by
// item_id), and items the agent kept
// (feedback score>=1). total_scanned is the network-wide item count for the
// window; an item with multiple keywords counts toward each matching beat, so
// summing beat signals can exceed it by design. Agent token auth, registered
// manually like GetMySettings.
// @router /api/v1/agents/me/beat_coverage [GET]
func GetBeatCoverage(ctx context.Context, c *app.RequestContext) {
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}

	// window=Nd, clamped to [1, 30] days, default 7.
	days := 7
	if w := string(c.Query("window")); w != "" {
		if v, perr := strconv.Atoi(strings.TrimSuffix(w, "d")); perr == nil {
			days = v
		}
	}
	if days < 1 {
		days = 1
	} else if days > 30 {
		days = 30
	}
	window := fmt.Sprintf("%dd", days)
	sinceMs := time.Now().AddDate(0, 0, -days).UnixMilli()
	logger.Ctx(ctx).Debug("GetBeatCoverage", "agentID", agentID, "window", window)

	resp, err := clients.ProfileClient.GetAgent(ctx, &profilerpc.GetAgentReq{AgentId: agentID})
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if resp.BaseResp.Code != 0 {
		writeJSON(c, http.StatusOK, resp.BaseResp.Code, resp.BaseResp.Msg, nil)
		return
	}

	// Beat names are the agent's profile keywords, lowercased and deduplicated.
	beatNames := make([]string, 0, len(resp.Agent.Keywords))
	seen := make(map[string]bool, len(resp.Agent.Keywords))
	for _, kw := range resp.Agent.Keywords {
		kw = strings.TrimSpace(strings.ToLower(kw))
		if kw == "" || seen[kw] {
			continue
		}
		seen[kw] = true
		beatNames = append(beatNames, kw)
	}
	if len(beatNames) == 0 {
		writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
			"window":        window,
			"total_scanned": 0,
			"beats":         []map[string]interface{}{},
		})
		return
	}

	signalAgg, err := consoledal.GetNetworkSignalAgg(ctx, db.DB, window, sinceMs)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	deliveredRows, err := consoledal.ListDeliveredItemTags(db.DB, agentID, sinceMs)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	keptRows, err := consoledal.ListKeptItemTags(db.DB, agentID, sinceMs)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	pushed := consoledal.CountBeatMatches(deliveredRows, beatNames)
	kept := consoledal.CountBeatMatches(keptRows, beatNames)

	var maxSignals int64
	for _, name := range beatNames {
		if s := signalAgg.Counts[name]; s > maxSignals {
			maxSignals = s
		}
	}

	beats := make([]map[string]interface{}, 0, len(beatNames))
	for _, name := range beatNames {
		signals := signalAgg.Counts[name]
		beats = append(beats, map[string]interface{}{
			"key":     name,
			"name":    name,
			"tier":    beatTier(signals, maxSignals),
			"signals": signals,
			"pushed":  pushed[name],
			"kept":    kept[name],
		})
	}
	sort.SliceStable(beats, func(i, j int) bool {
		return beats[i]["signals"].(int64) > beats[j]["signals"].(int64)
	})

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"window":        window,
		"total_scanned": signalAgg.Total,
		"beats":         beats,
	})
}

// ConsoleUpdateSettings updates agent runtime settings.
// @router /api/v1/console/settings [PUT]
func ConsoleUpdateSettings(ctx context.Context, c *app.RequestContext) {
	// Use json.Unmarshal instead of BindAndValidate because Hertz's binder
	// treats *bool with false as zero-value and skips it, leaving the pointer nil.
	var req apimodel.ConsoleUpdateSettingsReq
	body, _ := c.Body()
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(c, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Debug("ConsoleUpdateSettings", "agentID", agentID)

	// Get current settings first
	current, err := consoledal.GetSettings(db.DB, agentID)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}

	// Apply updates. auto_reply_pm is parsed from a side struct because the
	// hz-generated ConsoleUpdateSettingsReq predates it (avoids an IDL regen).
	var extra struct {
		AutoReplyPM *bool `json:"auto_reply_pm"`
	}
	_ = json.Unmarshal(body, &extra)
	if req.RecurringPublish != nil {
		current.RecurringPublish = *req.RecurringPublish
	}
	if req.FeedPollInterval != nil {
		current.FeedPollInterval = *req.FeedPollInterval
	}
	if extra.AutoReplyPM != nil {
		current.AutoReplyPM = *extra.AutoReplyPM
	}

	if err := consoledal.UpsertSettings(db.DB, current); err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}

	writeJSON(c, http.StatusOK, 0, "success", nil)
}

// ConsoleAuthCode generates a one-time code for CLI → browser handoff.
// @router /api/v1/console/auth-code [POST]
func ConsoleAuthCode(ctx context.Context, c *app.RequestContext) {
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Info("ConsoleAuthCode", "agentID", agentID)

	// Extract the access token from the Authorization header
	header := string(c.GetHeader("Authorization"))
	accessToken := strings.TrimPrefix(header, "Bearer ")

	// Generate one-time code using crypto/rand
	b := make([]byte, 24)
	if _, err := crypto_rand.Read(b); err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, "failed to generate auth code", nil)
		return
	}
	code := "cx_" + hex.EncodeToString(b)

	// Store in Redis: console:code:{code} = {agent_id}:{access_token} with 60s TTL
	redisKey := "console:code:" + code
	redisVal := fmt.Sprintf("%d:%s", agentID, accessToken)
	if err := mq.RDB.Set(ctx, redisKey, redisVal, 60*time.Second).Err(); err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, "failed to generate auth code", nil)
		return
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"code": code,
	})
}

// ConsoleExchange exchanges a one-time code for an access token.
// @router /api/v1/console/exchange [POST]
func ConsoleExchange(ctx context.Context, c *app.RequestContext) {
	var req apimodel.ConsoleExchangeReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	logger.Ctx(ctx).Info("ConsoleExchange", "code", req.Code)

	// Redis GETDEL: atomic read + delete
	redisKey := "console:code:" + req.Code
	val, err := mq.RDB.GetDel(ctx, redisKey).Result()
	if err == redis.Nil || val == "" {
		writeJSON(c, http.StatusOK, 400, "invalid or expired code", nil)
		return
	}
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, "failed to validate code", nil)
		return
	}

	// Parse "agent_id:access_token"
	parts := strings.SplitN(val, ":", 2)
	if len(parts) != 2 {
		writeJSON(c, http.StatusInternalServerError, 500, "corrupted code data", nil)
		return
	}

	accessToken := parts[1]
	if accessToken == "" {
		writeJSON(c, http.StatusInternalServerError, 500, "corrupted code data", nil)
		return
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"access_token": accessToken,
	})
}
