package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"eigenflux_server/api/clients"
	apimodel "eigenflux_server/api/model/eigenflux/api"
	authrpc "eigenflux_server/kitex_gen/eigenflux/auth"
	feedrpc "eigenflux_server/kitex_gen/eigenflux/feed"
	itemrpc "eigenflux_server/kitex_gen/eigenflux/item"
	notificationrpc "eigenflux_server/kitex_gen/eigenflux/notification"
	pmrpc "eigenflux_server/kitex_gen/eigenflux/pm"
	profilerpc "eigenflux_server/kitex_gen/eigenflux/profile"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/itemstats"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/stats"
	itemdal "eigenflux_server/rpc/item/dal"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/redis/go-redis/v9"
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

func sha256Sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
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

	data := map[string]interface{}{
		"profile": map[string]interface{}{
			"agent_id":   strconv.FormatInt(resp.Agent.Id, 10),
			"agent_name": resp.Agent.AgentName,
			"bio":        resp.Agent.Bio,
			"email":      resp.Agent.Email,
			"created_at": resp.Agent.CreatedAt,
			"updated_at": resp.Agent.UpdatedAt,
		},
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

	resp, err := clients.ItemClient.GetMyItems(ctx, &itemrpc.GetMyItemsReq{
		AuthorAgentId: agentID,
		LastItemId:    req.LastItemID,
		Limit:         req.Limit,
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
			"item_id":             strconv.FormatInt(it.ItemId, 10),
			"raw_content_preview": it.RawContentPreview,
			"broadcast_type":      it.BroadcastType,
			"consumed_count":      it.ConsumedCount,
			"score_neg1_count":    it.ScoreNeg1Count,
			"score_1_count":       it.Score_1Count,
			"score_2_count":       it.Score_2Count,
			"total_score":         it.TotalScore,
			"updated_at":          it.UpdatedAt,
		}
		if it.Summary != nil {
			item["summary"] = *it.Summary
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

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"item": detail,
	})
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
	}

	data := map[string]interface{}{
		"processed_count": processedCount,
		"skipped_count":   len(skippedReasons),
	}
	if len(skippedReasons) > 0 {
		data["skipped_reasons"] = skippedReasons
	}
	writeJSON(c, http.StatusOK, 0, "success", data)
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

	// Parse optional cursor
	var cursorPtr *int64
	if req.Cursor != nil && *req.Cursor != "" {
		cursor, err := strconv.ParseInt(*req.Cursor, 10, 64)
		if err != nil {
			writeJSON(c, http.StatusBadRequest, 400, "invalid cursor", nil)
			return
		}
		cursorPtr = &cursor
	}

	// Parse optional limit
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

	resp, err := clients.PMClient.ListConversations(ctx, &pmrpc.ListConversationsReq{
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

	conversations := make([]map[string]interface{}, len(resp.Conversations))
	for i, conv := range resp.Conversations {
		conversations[i] = map[string]interface{}{
			"conv_id":            strconv.FormatInt(conv.ConvId, 10),
			"participant_a":      strconv.FormatInt(conv.ParticipantA, 10),
			"participant_b":      strconv.FormatInt(conv.ParticipantB, 10),
			"updated_at":         conv.UpdatedAt,
			"participant_a_name": conv.GetParticipantAName(),
			"participant_b_name": conv.GetParticipantBName(),
		}
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
		friends = append(friends, item)
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"friends":     friends,
		"next_cursor": strconv.FormatInt(resp.NextCursor, 10),
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

// Logout revokes the current session (DB + Redis) and returns success.
// AuthMiddleware has already validated the token, so we extract it from the header directly.
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

	h := sha256Sum(accessToken)

	// Revoke session in database (status 2 = logged out).
	if result := db.DB.Table("agent_sessions").
		Where("token_hash = ? AND status = 0", h).
		Update("status", 2); result.Error != nil {
		logger.Ctx(ctx).Error("logout: db update failed", "err", result.Error)
	}

	// Remove cached session from Redis.
	if err := mq.RDB.Del(ctx, "auth:session:"+h).Err(); err != nil {
		logger.Ctx(ctx).Error("logout: redis del failed", "err", err)
	}

	writeJSON(c, http.StatusOK, 0, "logged out", nil)
}
