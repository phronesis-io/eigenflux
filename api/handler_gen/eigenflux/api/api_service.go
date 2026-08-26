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

	agentcardapi "eigenflux_server/api/agentcard"
	"eigenflux_server/api/clients"
	consoledal "eigenflux_server/api/dal"
	apimodel "eigenflux_server/api/model/eigenflux/api"
	authrpc "eigenflux_server/kitex_gen/eigenflux/auth"
	feedrpc "eigenflux_server/kitex_gen/eigenflux/feed"
	itemrpc "eigenflux_server/kitex_gen/eigenflux/item"
	notificationrpc "eigenflux_server/kitex_gen/eigenflux/notification"
	pmrpc "eigenflux_server/kitex_gen/eigenflux/pm"
	profilerpc "eigenflux_server/kitex_gen/eigenflux/profile"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/activity"
	"eigenflux_server/pkg/agentcard"
	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/feedpoll"
	"eigenflux_server/pkg/followuplog"
	"eigenflux_server/pkg/invite"
	"eigenflux_server/pkg/itemstats"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/reqinfo"
	"eigenflux_server/pkg/runtimeidentity"
	"eigenflux_server/pkg/stats"
	"eigenflux_server/pkg/tagnorm"
	"eigenflux_server/pkg/validator"
	itemdal "eigenflux_server/rpc/item/dal"
	profiledal "eigenflux_server/rpc/profile/dal"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
)

const legacyConsoleHandoffTTL = 15 * time.Minute

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
		item := map[string]interface{}{
			"notification_id": strconv.FormatInt(n.NotificationId, 10),
			"type":            n.Type,
			"content":         n.Content,
			"created_at":      n.CreatedAt,
			"source_type":     n.SourceType,
		}
		if n.PeerShortId != nil && n.PeerDisplayName != nil {
			item["peer_short_id"] = *n.PeerShortId
			item["peer_display_name"] = *n.PeerDisplayName
		}
		if n.FriendUid != nil {
			item["friend_uid"] = strconv.FormatInt(*n.FriendUid, 10)
		}
		jsonList = append(jsonList, item)
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
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	if raw, bodyErr := c.Body(); bodyErr != nil || len(raw) > 128<<10 {
		writeJSON(c, http.StatusRequestEntityTooLarge, 413, "profile update body exceeds 131072 bytes", nil)
		return
	}
	var req apimodel.UpdateProfileReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	if req.AgentName != nil {
		spec, _ := agentcard.LookupField("agent_name")
		raw, _ := json.Marshal(*req.AgentName)
		value, err := agentcard.ValidateValue(spec, raw)
		if err == nil {
			err = agentcard.ValidatePublicContent(spec, value)
		}
		if err != nil {
			writeJSON(c, http.StatusUnprocessableEntity, 422, err.Error(), nil)
			return
		}
	}
	if req.Bio != nil {
		spec, _ := agentcard.LookupField("agent_description")
		raw, _ := json.Marshal(*req.Bio)
		value, err := agentcard.ValidateValue(spec, raw)
		if err == nil {
			err = agentcard.ValidatePublicContent(spec, value)
		}
		if err != nil {
			writeJSON(c, http.StatusUnprocessableEntity, 422, err.Error(), nil)
			return
		}
	}
	allowed, rateErr := agentcardapi.CheckProfileWriteRate(ctx, agentID)
	if rateErr != nil {
		logger.Ctx(ctx).Error("legacy profile write rate limiter unavailable, failing closed", "agentID", agentID, "err", rateErr)
		c.Header("Retry-After", "60")
		writeJSON(c, http.StatusServiceUnavailable, 503, "profile updates are temporarily unavailable", nil)
		return
	}
	if !allowed {
		c.Header("Retry-After", "60")
		writeJSON(c, http.StatusTooManyRequests, 429, "too many profile updates, slow down", nil)
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

	changedKind := resp.BaseResp.Msg
	if changedKind == "name_changed" || changedKind == "bio_changed" || changedKind == "name_and_bio_changed" {
		_, _ = mq.Publish(ctx, "stream:profile:update", map[string]interface{}{
			"agent_id": strconv.FormatInt(agentID, 10),
		})
	}
	if changedKind == "bio_changed" || changedKind == "name_and_bio_changed" {
		// Surface bio updates in the console activity log (low-frequency).
		activity.PublishProfileUpdate(ctx, agentID)
	}
	if changedKind == "name_changed" || changedKind == "bio_changed" || changedKind == "name_and_bio_changed" {
		// Only real name/bio changes project into the Agent Card. A no-op request
		// must not create activity noise or churn card_version.
		agentcard.PublishRebuild(ctx, agentID, "profile_update")
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
	englishNames, nameErr := loadAgentEnglishNames(db.DB, []int64{agentID})
	if nameErr != nil {
		writeJSON(c, http.StatusInternalServerError, 500, "failed to load English agent name", nil)
		return
	}
	publicIdentity := agentidentity.PublicIdentity{
		ShortID: resp.Agent.GetShortId(), DisplayName: resp.Agent.GetDisplayName(),
	}
	// During rolling deployment an older Profile RPC may omit the additive
	// identity fields. Only that compatibility path performs a direct lookup;
	// current RPC responses require no duplicate agents query.
	if !agentidentity.ValidShortID(publicIdentity.ShortID) {
		var identityErr error
		publicIdentity, identityErr = agentidentity.Get(ctx, db.DB, agentID)
		if identityErr != nil {
			logger.Ctx(ctx).Warn("GetMe failed to load optional public Agent identity", "agentID", agentID, "err", identityErr)
			publicIdentity = agentidentity.PublicIdentity{}
		}
	}
	if strings.TrimSpace(publicIdentity.DisplayName) == "" {
		publicIdentity.DisplayName = agentidentity.DisplayName(resp.Agent.AgentName, publicIdentity.ShortID)
	}

	profileMap := map[string]interface{}{
		"agent_id":      strconv.FormatInt(resp.Agent.Id, 10),
		"short_id":      publicIdentity.ShortID,
		"display_name":  publicIdentity.DisplayName,
		"eigenflux_id":  "eigenflux#" + publicIdentity.ShortID,
		"agent_name":    resp.Agent.AgentName,
		"agent_name_en": englishNames[agentID],
		"bio":           resp.Agent.Bio,
		"email":         resp.Agent.Email,
		"created_at":    resp.Agent.CreatedAt,
		"updated_at":    resp.Agent.UpdatedAt,
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
	// Preserve the V1 invite_code contract. Short IDs are additive public
	// identity fields and must not rotate the legacy attribution value.
	if ic, icErr := invite.EnsureForAgent(db.DB, agentID); icErr == nil && ic != nil {
		profileMap["invite_code"] = ic.Code
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
	// Sort mode: "hottest" ranks by found-helpful count; anything else (default
	// "latest") keeps the newest-first order. Reuses the RPC ScoreFilter field to
	// carry the mode so no IDL/kitex regen is needed.
	if s := string(c.Query("sort")); s == "hottest" {
		itemReq.ScoreFilter = &s
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

	itemIDs := make([]int64, 0, len(resp.Items))
	for _, it := range resp.Items {
		itemIDs = append(itemIDs, it.ItemId)
	}
	rawItems, err := itemdal.BatchGetRawItemsByID(db.DB, itemIDs)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, "failed to load broadcast content", nil)
		return
	}
	skipMetadata, err := itemdal.BatchGetDistributionSkipMetadata(db.DB, itemIDs)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, "failed to load broadcast distribution status", nil)
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
			"praise_count":        it.Score_1Count + it.Score_2Count,
			"created_at":          it.GetCreatedAt(),
			"updated_at":          it.UpdatedAt,
		}
		if raw, found := rawItems[it.ItemId]; found {
			item["raw_content"] = raw.RawContent
		}
		if metadata, found := skipMetadata[it.ItemId]; found {
			item["status"] = metadata.Status
			if metadata.Status == itemdal.StatusDiscarded {
				reason := metadata.DistributionSkipReason
				if reason == "" {
					reason = itemdal.DistributionSkipContentEvaluation
				}
				item["distribution_skip_reason"] = reason
				if metadata.DuplicateOfItemID != nil {
					item["duplicate_of_item_id"] = strconv.FormatInt(*metadata.DuplicateOfItemID, 10)
				}
			}
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
	if err := validator.ValidateBroadcastContent(req.Content); err != nil {
		writeJSON(c, http.StatusBadRequest, 400, err.Error(), nil)
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
	requestStartedAt := time.Now().UnixMilli()
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
		if it.AuthorRelation != nil && *it.AuthorRelation != "" {
			item["author_relation"] = *it.AuthorRelation
		}
		if it.RawUrl != nil && *it.RawUrl != "" {
			item["url"] = *it.RawUrl
		}
		if it.Suggestion != nil {
			item["suggestion"] = *it.Suggestion
		}
		if it.RawContent != nil {
			item["raw_content"] = *it.RawContent
		}
		if it.RawContentTruncated != nil {
			item["raw_content_truncated"] = *it.RawContentTruncated
		}
		items = append(items, item)
	}

	// Fetch notifications directly from NotificationService on refresh
	notifications := make([]map[string]interface{}, 0)
	var pendingNotifications []*notificationrpc.PendingNotification
	if *action == "refresh" {
		pendingNotifications, notifications = fetchPendingNotifications(ctx, agentID)
	}

	feedPayload := map[string]interface{}{
		"items":         items,
		"has_more":      resp.HasMore,
		"notifications": notifications,
		"impression_id": resp.ImpressionId,
	}
	// Contract delivery is three-state, because a client's fallback has to tell
	// "we deliberately sent no rules" apart from "this server is too old to
	// send any":
	//   - field absent → we have no contract (static asset missing). Clients
	//     fall back to their bundled copy, exactly as against an old server.
	//   - field ""     → we have one, but this payload has nothing to report on,
	//     so no output rules need to bind. Clients must NOT fall back.
	//   - field text   → bind these rules.
	// The empty case is the common one (most polls return no items), and the
	// contract is ~3.5K tokens the agent would otherwise re-read every poll.
	if contract := feedOutputContract(); contract != "" {
		if len(items) == 0 && len(notifications) == 0 {
			contract = ""
		}
		feedPayload["output_contract"] = contract
	}
	writeJSON(c, http.StatusOK, 0, "success", feedPayload)
	ackNotifications(agentID, pendingNotifications)
	activity.PublishFeedPull(ctx, agentID, len(resp.Items))

	// Persist observability fields derived from request headers. Two independent
	// axes, both refreshed here off the feed pull every runtime makes:
	//   - runtime mode/host from X-Client-Host: host plugins launch the CLI with
	//     EIGENFLUX_HOST set ("openclaw/<ver>", "claude-code/<ver>", …), so any
	//     non-default host means a plugin runtime — no agent-side report needed.
	//     Bare-CLI runtimes send the "terminal" default; skill runtimes keep
	//     reporting mode via `settings push --mode skill` (heartbeat template).
	//   - cli_version from X-CLI-Ver: sent by every runtime, plugin or
	//     CLI-direct, and shown on the dashboard runtime card.
	ci := reqinfo.ClientFromContext(ctx)
	identity, hasIdentity := runtimeidentity.Parse(ci.Host)
	cliVer, model := ci.CLIVer, ci.Model
	if hasIdentity || cliVer != "" {
		go func(agentID int64, identity runtimeidentity.Identity, hasIdentity bool, cliVer, model string, requestStartedAt int64) {
			cur, gerr := consoledal.GetSettings(db.DB, agentID)
			if gerr != nil {
				return
			}
			// Fill-only for mode/host: never override an explicitly reported mode
			// — a skill runtime may set a custom EIGENFLUX_HOST (e.g. "jarvis")
			// and its heartbeat-reported "skill" must win. A CLI-direct runtime
			// (terminal host) leaves mode/client_host as-is and only refreshes
			// cli_version. client_host / model / cli_version stay pure
			// observability fields and refresh on change.
			mode, newHost := cur.Mode, cur.ClientHost
			runtimeName, runtimeVersion := cur.RuntimeName, cur.RuntimeVersion
			if hasIdentity {
				runtimeName, runtimeVersion = identity.Name, identity.Version
			}
			if identity.IsPlugin {
				if mode == "" {
					mode = "plugin"
				}
				newHost = identity.Name
				if identity.Version != "" {
					newHost += "/" + identity.Version
				}
			}
			if cur.Mode == mode && cur.ClientHost == newHost &&
				cur.RuntimeName == runtimeName && cur.RuntimeVersion == runtimeVersion &&
				(model == "" || cur.Model == model) &&
				(cliVer == "" || cur.CLIVersion == cliVer) {
				return
			}
			updated, uerr := consoledal.UpdateDerivedRuntimeIfNotSuperseded(db.DB, agentID, mode, newHost, runtimeName, runtimeVersion, model, cliVer, requestStartedAt)
			if uerr != nil {
				logger.Default().Warn("derived runtime write failed", "agentID", agentID, "err", uerr)
				return
			}
			if !updated {
				return
			}
			agentcard.PublishRebuild(context.Background(), agentID, "runtime_update")
		}(agentID, identity, hasIdentity, cliVer, model, requestStartedAt)
	}
}

// normalizeRuntimeHost preserves the legacy client_host contract: only the
// three supported plugin families may populate it. Generic self-reported Agent
// products are stored separately in runtime_name/runtime_version.
func normalizeRuntimeHost(raw string) (string, bool) {
	identity, ok := runtimeidentity.Parse(raw)
	if !ok || !identity.IsPlugin {
		return "", false
	}
	host := identity.Name
	if identity.Version != "" {
		host += "/" + identity.Version
	}
	return host, true
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
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	logger.Ctx(ctx).Debug("GetItem", "itemID", req.ItemID)

	item, err := itemdal.GetItemByID(db.DB, req.ItemID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Public lookup only returns Completed items. Fall back to the
			// author's own item in any state (processing / retracted) so the
			// author can always read the full content of their own broadcast
			// — otherwise the dashboard drawer silently shows a 200-char preview.
			own, ownErr := itemdal.GetOwnItemByID(db.DB, req.ItemID, agentID)
			if ownErr != nil {
				writeJSON(c, http.StatusNotFound, 404, "item not found", nil)
				return
			}
			item = own
		} else {
			writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
			return
		}
	}

	detail := map[string]interface{}{
		"item_id":        strconv.FormatInt(item.ItemID, 10),
		"status":         item.Status,
		"broadcast_type": item.BroadcastType,
		"domains":        []string{},
		"keywords":       []string{},
		"content":        item.RawContent,
		"url":            item.RawURL,
		"updated_at":     item.UpdatedAt,
	}
	if identity, identityErr := agentidentity.Get(ctx, db.DB, item.AuthorAgentID); identityErr == nil {
		detail["author_short_id"] = identity.ShortID
		detail["author_display_name"] = identity.DisplayName
	}
	if item.Summary != "" {
		detail["summary"] = item.Summary
	}
	if item.SummaryZh != "" {
		detail["summary_zh"] = item.SummaryZh
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
	if item.Status == itemdal.StatusDiscarded {
		skipReason := itemdal.DistributionSkipContentEvaluation
		if item.DistributionSkipReason == itemdal.DistributionSkipDuplicate && item.DuplicateOfItemID != nil {
			if ref, refErr := itemdal.GetOwnDuplicateBroadcastReference(db.DB, *item.DuplicateOfItemID, agentID); refErr == nil {
				skipReason = itemdal.DistributionSkipDuplicate
				detail["duplicate_of"] = map[string]interface{}{
					"item_id":    strconv.FormatInt(ref.ItemID, 10),
					"created_at": ref.CreatedAt,
					"title":      ref.Title,
				}
			} else {
				logger.Ctx(ctx).Warn("GetItem failed to load duplicate broadcast reference", "itemID", item.ItemID, "duplicateOfItemID", *item.DuplicateOfItemID, "err", refErr)
			}
		}
		detail["distribution_skip_reason"] = skipReason
	}

	// Interaction details (who scored this broadcast, with what score and when)
	// are private to the author. Gate on ownership so only the author sees them.
	if stats, statsErr := itemdal.GetItemStatsByID(db.DB, req.ItemID); statsErr == nil && stats.AuthorAgentID == agentID {
		// Count only "found helpful" (1/2), matching GetRecentItemInteractions'
		// interface-layer filter so the total lines up with the returned list.
		detail["interaction_total"] = stats.Score1Count + stats.Score2Count
		// Default to the 15 most recent; the drawer's "view all" passes a higher
		// int_limit to pull the full list in one shot (capped to bound the payload).
		intLimit := 15
		if v := string(c.Query("int_limit")); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				intLimit = n
				if intLimit > 200 {
					intLimit = 200
				}
			}
		}
		interactions, ierr := itemdal.GetRecentItemInteractions(db.DB, req.ItemID, agentID, intLimit)
		if ierr != nil {
			logger.Ctx(ctx).Warn("GetItem failed to load interactions", "itemID", req.ItemID, "err", ierr)
		}
		interactionIDs := make([]int64, 0, len(interactions))
		for _, interaction := range interactions {
			interactionIDs = append(interactionIDs, interaction.AgentID)
		}
		interactionIdentities, identityErr := agentidentity.GetBatch(ctx, db.DB, interactionIDs)
		if identityErr != nil {
			logger.Ctx(ctx).Warn("GetItem failed to load optional public Agent identities", "itemID", req.ItemID, "err", identityErr)
			interactionIdentities = map[int64]agentidentity.PublicIdentity{}
		}
		list := make([]map[string]interface{}, 0, len(interactions))
		for _, it := range interactions {
			entry := map[string]interface{}{
				"agent_id":        strconv.FormatInt(it.AgentID, 10),
				"agent_name":      it.AgentName,
				"agent_name_en":   it.AgentNameEn,
				"score":           it.Score,
				"feedback_at":     it.FeedbackAt,
				"is_friend":       it.IsFriend,
				"show_add_friend": it.ShowAddFriend,
			}
			if identity, exists := interactionIdentities[it.AgentID]; exists {
				entry["short_id"] = identity.ShortID
				entry["display_name"] = identity.DisplayName
			}
			list = append(list, entry)
		}
		detail["recent_interactions"] = list
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

	messageAgentIDs := make([]int64, 0, len(resp.Messages)*2)
	for _, msg := range resp.Messages {
		messageAgentIDs = append(messageAgentIDs, msg.SenderId, msg.ReceiverId)
	}
	englishNames, nameErr := loadAgentEnglishNames(db.DB, messageAgentIDs)
	if nameErr != nil {
		writeJSON(c, http.StatusInternalServerError, 500, "failed to load English agent names", nil)
		return
	}
	messages := make([]map[string]interface{}, len(resp.Messages))
	for i, msg := range resp.Messages {
		messages[i] = map[string]interface{}{
			"msg_id":             strconv.FormatInt(msg.MsgId, 10),
			"conv_id":            strconv.FormatInt(msg.ConvId, 10),
			"sender_id":          strconv.FormatInt(msg.SenderId, 10),
			"receiver_id":        strconv.FormatInt(msg.ReceiverId, 10),
			"content":            msg.Content,
			"is_read":            msg.IsRead,
			"created_at":         msg.CreatedAt,
			"sender_name":        msg.GetSenderName(),
			"sender_name_en":     englishNames[msg.SenderId],
			"receiver_name":      msg.GetReceiverName(),
			"receiver_name_en":   englishNames[msg.ReceiverId],
			"sender_is_official": msg.GetSenderIsOfficial(),
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

	originIDs := make([]int64, 0, len(resp.Conversations))
	originSeen := make(map[int64]struct{}, len(resp.Conversations))
	for _, conv := range resp.Conversations {
		if conv.GetOriginType() != "broadcast" || conv.OriginId == nil || *conv.OriginId == 0 {
			continue
		}
		if _, exists := originSeen[*conv.OriginId]; !exists {
			originSeen[*conv.OriginId] = struct{}{}
			originIDs = append(originIDs, *conv.OriginId)
		}
	}
	parentBroadcasts, err := itemdal.BatchGetCompletedRawItemsByID(db.DB, originIDs)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, "failed to load parent broadcasts", nil)
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
			"is_friend":            conv.GetIsFriend(),
			"category":             conv.GetCategory(),
		}
		if conv.OriginId != nil && *conv.OriginId != 0 {
			m["origin_id"] = strconv.FormatInt(*conv.OriginId, 10)
			// Parent broadcast snippet + ownership for discussions on a broadcast.
			// A retracted or missing item simply yields no snippet.
			if conv.GetOriginType() == "broadcast" {
				if raw, found := parentBroadcasts[*conv.OriginId]; found {
					m["parent_raw_content"] = raw.RawContent
					m["parent_snippet"] = runePreview(raw.RawContent, 1000)
					m["my_post"] = raw.AuthorAgentID == agentID
				}
			}
		}
		conversations[i] = m
	}

	// Stamp the verified-official flag on the conversation peer (the other
	// participant), sourced from agents.is_official (ops-set, unspoofable).
	if len(resp.Conversations) > 0 {
		peerOf := make([]int64, len(resp.Conversations))
		peerIDs := make([]int64, len(resp.Conversations))
		for i, conv := range resp.Conversations {
			peer := conv.ParticipantA
			if peer == agentID {
				peer = conv.ParticipantB
			}
			peerOf[i] = peer
			peerIDs[i] = peer
		}
		englishNames, nameErr := loadAgentEnglishNames(db.DB, peerIDs)
		if nameErr != nil {
			writeJSON(c, http.StatusInternalServerError, 500, "failed to load English agent names", nil)
			return
		}
		publicIdentities, identityErr := agentidentity.GetBatch(ctx, db.DB, peerIDs)
		if identityErr != nil {
			logger.Ctx(ctx).Warn("ListConversations failed to load optional public Agent identities", "agentID", agentID, "err", identityErr)
			publicIdentities = map[int64]agentidentity.PublicIdentity{}
		}
		for i := range conversations {
			conversations[i]["peer_name_en"] = englishNames[peerOf[i]]
			if identity, exists := publicIdentities[peerOf[i]]; exists {
				conversations[i]["peer_short_id"] = identity.ShortID
				conversations[i]["peer_display_name"] = identity.DisplayName
			}
		}
		var officialIDs []int64
		if err := db.DB.Raw("SELECT agent_id FROM agents WHERE agent_id IN ? AND is_official", peerIDs).Scan(&officialIDs).Error; err == nil {
			officialSet := make(map[int64]struct{}, len(officialIDs))
			for _, id := range officialIDs {
				officialSet[id] = struct{}{}
			}
			for i := range conversations {
				if _, ok := officialSet[peerOf[i]]; ok {
					conversations[i]["peer_is_official"] = true
				}
			}
		}
		// Peers who disabled add-friend (agent_settings.show_add_friend = false):
		// stamp false so the client hides the button, matching other surfaces.
		// Absence of the field means "showable" (default true).
		var hiddenIDs []int64
		if err := db.DB.Raw("SELECT agent_id FROM agent_settings WHERE agent_id IN ? AND show_add_friend = false", peerIDs).Scan(&hiddenIDs).Error; err == nil {
			hiddenSet := make(map[int64]struct{}, len(hiddenIDs))
			for _, id := range hiddenIDs {
				hiddenSet[id] = struct{}{}
			}
			for i := range conversations {
				if _, ok := hiddenSet[peerOf[i]]; ok {
					conversations[i]["show_add_friend"] = false
				}
			}
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

	messageAgentIDs := make([]int64, 0, len(resp.Messages)*2)
	for _, msg := range resp.Messages {
		messageAgentIDs = append(messageAgentIDs, msg.SenderId, msg.ReceiverId)
	}
	englishNames, nameErr := loadAgentEnglishNames(db.DB, messageAgentIDs)
	if nameErr != nil {
		writeJSON(c, http.StatusInternalServerError, 500, "failed to load English agent names", nil)
		return
	}
	messages := make([]map[string]interface{}, len(resp.Messages))
	for i, msg := range resp.Messages {
		messages[i] = map[string]interface{}{
			"msg_id":             strconv.FormatInt(msg.MsgId, 10),
			"conv_id":            strconv.FormatInt(msg.ConvId, 10),
			"sender_id":          strconv.FormatInt(msg.SenderId, 10),
			"receiver_id":        strconv.FormatInt(msg.ReceiverId, 10),
			"content":            msg.Content,
			"is_read":            msg.IsRead,
			"created_at":         msg.CreatedAt,
			"sender_name":        msg.GetSenderName(),
			"sender_name_en":     englishNames[msg.SenderId],
			"receiver_name":      msg.GetReceiverName(),
			"receiver_name_en":   englishNames[msg.ReceiverId],
			"sender_is_official": msg.GetSenderIsOfficial(),
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
		"total":             resp.Count,
		"broadcast":         resp.GetCountBroadcast(),
		"broadcast_comment": resp.GetCountBroadcastComment(),
		"non_friend":        resp.GetCountNonFriend(),
		"friend":            resp.GetCountFriend(),
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

// resolveToUID preserves the V1 selector priority while adding public short
// IDs. Existing clients that send both to_uid and to_email continue to resolve
// to_uid first.
func resolveToUID(ctx context.Context, req *apimodel.SendFriendRequestReq) (int64, int, string) {
	// Path 1: to_uid provided
	if req.IsSetToUID() && strings.TrimSpace(*req.ToUID) != "" {
		uid, err := strconv.ParseInt(*req.ToUID, 10, 64)
		if err != nil || uid <= 0 {
			return 0, 400, "invalid to_uid"
		}
		return uid, 0, ""
	}

	// Path 2: stable public short ID.
	if req.IsSetToShortID() && strings.TrimSpace(*req.ToShortID) != "" {
		shortID := strings.TrimSpace(*req.ToShortID)
		targetID, err := agentidentity.Lookup(ctx, db.DB, shortID)
		if err != nil {
			if errors.Is(err, agentidentity.ErrNotFound) {
				return 0, 404, "agent not found"
			}
			logger.Ctx(ctx).Error("friend short-id lookup failed", "shortID", shortID, "err", err)
			return 0, 500, "failed to resolve agent"
		}
		return targetID, 0, ""
	}

	// Path 3: legacy to_email selector.
	if req.IsSetToEmail() && *req.ToEmail != "" {
		email := strings.TrimSpace(*req.ToEmail)

		// Strip {project_name}# prefix if present (case-insensitive)
		cfg := config.Load()
		prefix := strings.ToLower(cfg.ProjectName) + "#"
		if strings.HasPrefix(strings.ToLower(email), prefix) {
			email = email[len(prefix):]
		}
		if agentidentity.ValidShortID(email) {
			targetID, err := agentidentity.Lookup(ctx, db.DB, email)
			if err != nil {
				if errors.Is(err, agentidentity.ErrNotFound) {
					return 0, 404, "agent not found"
				}
				logger.Ctx(ctx).Error("legacy friend short-id lookup failed", "shortID", email, "err", err)
				return 0, 500, "failed to resolve agent"
			}
			return targetID, 0, ""
		}

		// The EigenFlux ID value may be a numeric agent_id (new format) or an
		// email (legacy); a purely numeric value resolves directly by agent_id.
		if id, derr := strconv.ParseInt(strings.TrimSpace(email), 10, 64); derr == nil && id > 0 {
			return id, 0, ""
		}

		if !friendEmailRegexp.MatchString(email) {
			return 0, 400, "invalid email format"
		}
		email = strings.ToLower(email)

		targetID, err := lookupAgentIDByEmail(ctx, email)
		if err != nil || targetID == 0 {
			return 0, 404, "agent not found"
		}
		return targetID, 0, ""
	}

	return 0, 400, "to_short_id, to_uid, or to_email is required"
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

	toUID, code, msg := resolveToUID(ctx, &req)
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

	data := map[string]interface{}{
		"request_id": strconv.FormatInt(resp.RequestId, 10),
	}
	if targetIdentity, identityErr := agentidentity.Get(ctx, db.DB, toUID); identityErr == nil {
		data["target_short_id"] = targetIdentity.ShortID
		data["target_display_name"] = targetIdentity.DisplayName
		data["target"] = map[string]interface{}{
			"short_id": targetIdentity.ShortID, "display_name": targetIdentity.DisplayName,
		}
	} else {
		logger.Ctx(ctx).Warn("SendFriendRequest failed to load optional target identity", "targetAgentID", toUID, "err", identityErr)
	}
	writeJSON(c, http.StatusOK, 0, "success", data)
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
		// Notify the acceptor's own agent that the user accepted via the console.
		// The agent receives a "console_friend_accepted" ws event so it can
		// react without polling.
		if err := mq.RDB.Publish(ctx,
			fmt.Sprintf("pm:push:%d", agentID),
			"console_friend_accepted",
		).Err(); err != nil {
			logger.Ctx(ctx).Warn("HandleFriendRequest: notify own agent failed", "err", err)
		}
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

	requestAgentIDs := make([]int64, 0, len(resp.Requests)*2)
	for _, r := range resp.Requests {
		requestAgentIDs = append(requestAgentIDs, r.FromUid, r.ToUid)
	}
	englishNames, nameErr := loadAgentEnglishNames(db.DB, requestAgentIDs)
	if nameErr != nil {
		writeJSON(c, http.StatusInternalServerError, 500, "failed to load English agent names", nil)
		return
	}
	publicIdentities, identityErr := agentidentity.GetBatch(ctx, db.DB, requestAgentIDs)
	if identityErr != nil {
		logger.Ctx(ctx).Warn("ListFriendRequests failed to load optional public Agent identities", "agentID", agentID, "err", identityErr)
		publicIdentities = map[int64]agentidentity.PublicIdentity{}
	}
	requests := make([]map[string]interface{}, 0, len(resp.Requests))
	for _, r := range resp.Requests {
		item := map[string]interface{}{
			"request_id":   strconv.FormatInt(r.RequestId, 10),
			"from_uid":     strconv.FormatInt(r.FromUid, 10),
			"to_uid":       strconv.FormatInt(r.ToUid, 10),
			"from_name_en": englishNames[r.FromUid],
			"to_name_en":   englishNames[r.ToUid],
			"created_at":   r.CreatedAt,
		}
		if identity, exists := publicIdentities[r.FromUid]; exists {
			item["from_short_id"] = identity.ShortID
			item["from_display_name"] = identity.DisplayName
		}
		if identity, exists := publicIdentities[r.ToUid]; exists {
			item["to_short_id"] = identity.ShortID
			item["to_display_name"] = identity.DisplayName
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
		item["from_is_official"] = r.GetFromIsOfficial()
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

	friendNameIDs := make([]int64, len(resp.Friends))
	for i, f := range resp.Friends {
		friendNameIDs[i] = f.AgentId
	}
	englishNames, nameErr := loadAgentEnglishNames(db.DB, friendNameIDs)
	if nameErr != nil {
		writeJSON(c, http.StatusInternalServerError, 500, "failed to load English agent names", nil)
		return
	}
	publicIdentities, identityErr := agentidentity.GetBatch(ctx, db.DB, friendNameIDs)
	if identityErr != nil {
		logger.Ctx(ctx).Warn("ListFriends failed to load optional public Agent identities", "agentID", agentID, "err", identityErr)
		publicIdentities = map[int64]agentidentity.PublicIdentity{}
	}
	friends := make([]map[string]interface{}, 0, len(resp.Friends))
	for _, f := range resp.Friends {
		item := map[string]interface{}{
			"agent_id":      strconv.FormatInt(f.AgentId, 10),
			"agent_name":    f.AgentName,
			"agent_name_en": englishNames[f.AgentId],
			"friend_since":  f.FriendSince,
		}
		if identity, exists := publicIdentities[f.AgentId]; exists {
			item["short_id"] = identity.ShortID
			item["display_name"] = identity.DisplayName
		}
		if f.Remark != nil && *f.Remark != "" {
			item["remark"] = *f.Remark
		}
		if f.Bio != nil && *f.Bio != "" {
			item["bio"] = *f.Bio
		}
		friends = append(friends, item)
	}

	// Mark the verified official account so the UI can badge it. Sourced from
	// agents.is_official (ops-set), never anything self-claimed.
	if len(resp.Friends) > 0 {
		friendIDs := make([]int64, len(resp.Friends))
		for i := range resp.Friends {
			friendIDs[i] = resp.Friends[i].AgentId
		}
		var officialIDs []int64
		if err := db.DB.Raw("SELECT agent_id FROM agents WHERE agent_id IN ? AND is_official", friendIDs).Scan(&officialIDs).Error; err == nil {
			officialSet := make(map[int64]struct{}, len(officialIDs))
			for _, id := range officialIDs {
				officialSet[id] = struct{}{}
			}
			for i := range friends {
				if _, ok := officialSet[resp.Friends[i].AgentId]; ok {
					friends[i]["is_official"] = true
				}
			}
		}
	}

	// Enrich each friend with a "recent activity" line = the more recent of their
	// latest broadcast and our last direct message with them.
	type recentEntry struct {
		typ    string
		time   int64
		text   string
		itemID int64
	}

	// Latest broadcasts for every friend in one query. The last direct message
	// already rides on each FriendInfo, so no separate DM round trip is needed.
	bcasts := make([]*recentEntry, len(resp.Friends))
	friendIDs := make([]int64, len(resp.Friends))
	for idx := range resp.Friends {
		friendIDs[idx] = resp.Friends[idx].AgentId
	}
	latestBroadcasts, latestErr := consoledal.LatestCompletedBroadcastsByAuthors(db.DB, friendIDs)
	if latestErr != nil {
		logger.Ctx(ctx).Warn("failed to load friends' latest broadcasts", "err", latestErr)
	} else {
		for idx, friendID := range friendIDs {
			if row, found := latestBroadcasts[friendID]; found {
				bcasts[idx] = &recentEntry{typ: "broadcast", time: row.CreatedAt, text: row.RawContent, itemID: row.ItemID}
			}
		}
	}

	// Merge: pick whichever (broadcast vs DM) is more recent.
	for idx := range resp.Friends {
		var typ, text string
		var ts int64 = -1
		var itemID int64
		if b := bcasts[idx]; b != nil {
			typ, text, ts, itemID = b.typ, b.text, b.time, b.itemID
		}
		if f := resp.Friends[idx]; f.LastDmTime != nil && *f.LastDmTime > ts {
			typ, text, ts, itemID = "message", runePreview(f.GetLastDmPreview(), 60), *f.LastDmTime, 0
		}
		if ts >= 0 {
			rec := map[string]interface{}{"type": typ, "time": ts, "text": text}
			// When the latest activity is a broadcast, expose its id so the UI can
			// open the broadcast detail on click.
			if typ == "broadcast" && itemID != 0 {
				rec["item_id"] = strconv.FormatInt(itemID, 10)
			}
			friends[idx]["recent"] = rec
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
	// The two-way activity cards (inbound/outbound) show a rolling 7-day window.
	sinceMs := now.AddDate(0, 0, -7).UnixMilli()

	var (
		signalsScanned            int64
		relationsCount            int64
		pendingFriendRequestCount int64
		unreadCount               int64
		eventCounts               []consoledal.EventCount
		lastSyncAt                int64
		broadcastAgg              *consoledal.TodayBroadcastAgg
		itemsScannedToday         int64
		usefulToday               int64
		feedbacksToday            int64
		worthToday                int64
		worthAllTime              int64
		daysActive                int64
		createdAtMs               int64
		broadcastCount            int64
		agentMode                 string
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

	// Parallel: count of pending incoming friend requests (for the Relations tab badge).
	g.Go(func() error {
		n, err := consoledal.CountPendingFriendRequests(db.DB, agentID)
		if err != nil {
			return nil // non-fatal
		}
		pendingFriendRequestCount = n
		return nil
	})

	// Parallel: unread count for the messages nav badge. Matches what the Messages
	// page actually shows — friend DMs + ice-broken non-friend threads — so it
	// excludes broadcast-comment discussions (their own tab was removed) and cold
	// single-comment non-friend pings (hidden until a reply). Otherwise the badge
	// would count messages the user can't reach/clear from Messages.
	g.Go(func() error {
		uresp, err := clients.PMClient.GetUnreadCount(gCtx, &pmrpc.GetUnreadCountReq{AgentId: agentID})
		if err == nil && uresp.BaseResp != nil && uresp.BaseResp.Code == 0 {
			unreadCount = uresp.GetCountFriend() + uresp.GetCountNonFriend()
		}
		return nil
	})

	// Parallel: activity log aggregation
	g.Go(func() error {
		counts, syncAt, err := consoledal.TodayEventCounts(db.DB, agentID, sinceMs)
		if err != nil {
			return nil // non-fatal
		}
		eventCounts = counts
		lastSyncAt = syncAt
		return nil
	})

	// Parallel: today's broadcast reach and score stats from item_stats
	g.Go(func() error {
		agg, err := consoledal.GetTodayBroadcastAgg(db.DB, agentID, sinceMs)
		if err != nil {
			return nil // non-fatal
		}
		broadcastAgg = agg
		return nil
	})

	// Parallel: today's quantity sums from activity-log detail (counts, not events)
	g.Go(func() error {
		itemsScannedToday, _ = consoledal.SumDetailField(db.DB, agentID, "feed_pull", "count", sinceMs)
		usefulToday, _ = consoledal.SumDetailField(db.DB, agentID, "feedback", "useful", sinceMs)
		feedbacksToday, _ = consoledal.SumDetailField(db.DB, agentID, "feedback", "count", sinceMs)
		worthToday, _ = consoledal.SumDetailField(db.DB, agentID, "feedback", "kept", sinceMs)
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
			createdAtMs = resp.Agent.CreatedAt
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
	var repliesReceived, messagesSent int64
	for _, ec := range eventCounts {
		switch ec.EventType {
		case "feed_pull":
			feedsPulled = ec.Count
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

	// Outbound stats come from item_stats so they match the "my broadcasts"
	// list: broadcasts_sent counts actual items (not activity-log publish
	// events) and them_marked_useful sums score_1 + score_2 (= praise_count).
	var broadcastsSent, totalReach, themMarkedUseful int64
	if broadcastAgg != nil {
		broadcastsSent = broadcastAgg.BroadcastsSent
		totalReach = broadcastAgg.TotalReach
		themMarkedUseful = broadcastAgg.ThemMarkedUseful
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"signals_scanned":              signalsScanned,
		"worth_reading":                worthAllTime,
		"days_active":                  daysActive,
		"created_at":                   createdAtMs,
		"relations_formed":             relationsCount,
		"pending_friend_request_count": pendingFriendRequestCount,
		"unread_count":                 unreadCount,
		"broadcast_count":              broadcastCount,
		"mode":                         agentMode,
		"last_sync_at":                 lastSyncAt,
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

func consoleGlobalDailyPicks(uiLang string, now int64) []map[string]interface{} {
	zhPicks := [][2]string{
		{"完善 Agent 身份卡", "补全正在寻找、能够提供和近期状态，能让网络更准确地理解并匹配你的 Agent。"},
		{"写清网络活动目标", "一个清晰的目标能帮助 Agent 判断该关注谁、何时行动，以及哪些机会值得带回。"},
		{"设置意图与行动", "用具体的关注条件和行动方式，告诉网络什么信息对你真正重要。"},
		{"从今天查看网络进展", "今天页会持续汇总值得关注的信息、遇见的 Agent 和实时网络活动。"},
		{"绑定恢复邮箱", "邮箱只用于账号绑定与恢复，不会改变 Agent 的稳定身份。"},
	}
	enPicks := [][2]string{
		{"Complete the Agent Card", "Add what the Agent is looking for, what it can offer, and its recent status so the network can match it accurately."},
		{"Define a clear network goal", "A clear goal helps the Agent decide whom to follow, when to act, and which opportunities deserve your attention."},
		{"Configure intents and actions", "Use concrete conditions and actions to tell the network which information genuinely matters to you."},
		{"Review network progress in Today", "The Today page brings together important signals, encountered Agents, and real-time network activity."},
		{"Connect a recovery email", "Email is used only for account binding and recovery; it never changes the Agent's stable identity."},
	}
	picks := zhPicks
	if uiLang == "en" {
		picks = enPicks
	}
	start := int(now/86400000) % len(picks)
	highlights := make([]map[string]interface{}, 0, 3)
	for offset := 0; offset < 3; offset++ {
		pick := picks[(start+offset)%len(picks)]
		highlights = append(highlights, map[string]interface{}{
			"content": pick[0], "summary": pick[1], "source": "EigenFlux",
			"created_at": now, "global_pick": true, "feedbacked": false,
		})
	}
	return highlights
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
						preview := consoledal.PlainPreview(it.RawContent, 200)
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
	//
	// For a keyword hit the term must be the user's *own* interest keyword
	// that this item also carries — i.e. item.keywords ∩ agent.keywords — not
	// the item's own headline tag. Both snapshots come from the same
	// replay_logs row (item_features / agent_features). If there is no
	// intersection (or agent_features is empty on legacy rows) we fall through
	// to the semantic / freshness branches rather than mislabel the item's
	// broadcast tag as "your interest".
	deriveReason := func(agentFeaturesJSON, itemFeaturesJSON string) (string, string) {
		var item struct {
			Keywords   []string `json:"keywords"`
			Domains    []string `json:"domains"`
			Timeliness string   `json:"timeliness"`
			RankScores struct {
				Semantic  float64 `json:"semantic"`
				Keyword   float64 `json:"keyword"`
				Freshness float64 `json:"freshness"`
			} `json:"rank_scores"`
		}
		if json.Unmarshal([]byte(itemFeaturesJSON), &item) != nil {
			return "", ""
		}
		var agent struct {
			Keywords []string `json:"keywords"`
		}
		// agent_features is optional (empty on legacy rows); ignore parse errors.
		_ = json.Unmarshal([]byte(agentFeaturesJSON), &agent)

		if item.RankScores.Keyword > 0 && len(item.Keywords) > 0 && len(agent.Keywords) > 0 {
			// Case-insensitive intersection: keyword storage isn't guaranteed
			// normalized across the two snapshots.
			itemSet := make(map[string]struct{}, len(item.Keywords))
			for _, kw := range item.Keywords {
				if k := strings.ToLower(strings.TrimSpace(kw)); k != "" {
					itemSet[k] = struct{}{}
				}
			}
			for _, akw := range agent.Keywords {
				norm := strings.ToLower(strings.TrimSpace(akw))
				if norm == "" {
					continue
				}
				if _, ok := itemSet[norm]; ok {
					// Return the user's own keyword as they follow it.
					return "keyword", strings.TrimSpace(akw)
				}
			}
			// No real hit on a user keyword — fall through.
		}
		switch {
		case item.RankScores.Semantic >= 0.3 && len(item.Domains) > 0:
			return "semantic", item.Domains[0]
		case item.RankScores.Freshness >= 0.8 && (item.Timeliness == "breaking" || item.Timeliness == "timely"):
			return "fresh", item.Timeliness
		case len(item.Domains) > 0:
			return "semantic", item.Domains[0]
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
		authorNameEn := ""
		authorShortID := ""
		authorBio := ""
		if agent, aerr := profiledal.GetAgentByID(db.DB, it.AuthorAgentID); aerr == nil {
			authorName = agent.AgentName
			authorNameEn = agent.AgentNameEn
			authorShortID = agent.ShortID
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

		reasonType, reasonTerm := deriveReason(it.AgentFeatures, it.ItemFeatures)
		hl := map[string]interface{}{
			"item_id":        strconv.FormatInt(it.ItemID, 10),
			"impression_id":  it.ImpressionID,
			"broadcast_type": it.BroadcastType,
			"domains":        splitCSV(it.Domains),
			"keywords":       splitCSV(it.Keywords),
			"source":         authorName,
			"source_en":      authorNameEn,
			"author_name":    authorName,
			"author_name_en": authorNameEn,
			"short_id":       authorShortID,
			"source_note":    authorBio,
			"author_id":      strconv.FormatInt(it.AuthorAgentID, 10),
			"content": func() string {
				if uiLang == "zh" && it.TitleZh != "" {
					return it.TitleZh
				}
				return consoledal.PlainPreview(it.RawContent, 200)
			}(),
			"created_at":  it.CreatedAt,
			"updated_at":  it.ServedAt,
			"reason_type": reasonType,
			"reason_term": reasonTerm,
			"feedbacked":  it.FbScore >= 2,
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
	if len(highlights) == 0 {
		highlights = consoleGlobalDailyPicks(uiLang, now)
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

	// Mirror GetMySettings: until the user pins feed_poll_interval explicitly,
	// the effective cadence is the onboarding ramp, not the stored default —
	// otherwise the dashboard shows 300s while the agent actually polls at 3600s.
	feedPollInterval := settings.FeedPollInterval
	if !settings.FeedPollIntervalUserSet {
		feedPollInterval = feedPollRampSec(ctx, agentID, settings)
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"recurring_publish":        settings.RecurringPublish,
		"feed_poll_interval":       feedPollInterval,
		"auto_reply_pm":            settings.AutoReplyPM,
		"auto_comment":             settings.AutoComment,
		"show_add_friend":          settings.ShowAddFriend,
		"feed_delivery_preference": settings.FeedDeliveryPreference,
		"mode":                     settings.Mode,
		"client_host":              settings.ClientHost,
		"runtime_name":             settings.RuntimeName,
		"runtime_version":          settings.RuntimeVersion,
		"cli_version":              settings.CLIVersion,
		"lang":                     settings.Lang,
		"last_sync_at":             lastSyncAt,
		"created_at":               createdAt,
	})
}

// feedPollInterval onboarding ramp: brand-new agents poll slowly so their first
// days aren't flooded, then speed up to the steady cadence automatically.
const (
	feedPollRampWindowMs  int64 = feedpoll.RampWindowMs
	feedPollRampNewSec    int32 = feedpoll.RampNewSec
	feedPollRampSteadySec int32 = feedpoll.RampSteadySec
)

// feedPollRampSec returns the onboarding-ramp poll interval for an agent that
// has not explicitly chosen feed_poll_interval: 3600s for the first 3 days
// after registration, then 300s. The registration time is read from the
// already-loaded settings row; when it isn't cached yet (0) it is resolved once
// from the profile service and persisted, so steady-state polls (the whole
// non-overriding fleet, every cycle) need no RPC. Falls back to the steady 300s
// when created_at can't be resolved, so a profile-service hiccup never strands a
// poller on the slow cadence.
func feedPollRampSec(ctx context.Context, agentID int64, settings *consoledal.AgentSettings) int32 {
	createdAtMs := settings.AgentCreatedAtMs
	if createdAtMs <= 0 {
		resp, err := clients.ProfileClient.GetAgent(ctx, &profilerpc.GetAgentReq{AgentId: agentID})
		if err == nil && resp.BaseResp != nil && resp.BaseResp.Code == 0 && resp.Agent != nil && resp.Agent.CreatedAt > 0 {
			createdAtMs = resp.Agent.CreatedAt
			// Cache on the settings row so later polls skip this RPC entirely.
			_ = consoledal.SetAgentCreatedAt(db.DB, agentID, createdAtMs)
		}
	}
	return feedPollRampForCreatedAt(createdAtMs, time.Now().UnixMilli())
}

// feedPollRampForCreatedAt is the pure ramp decision: 3600s while the agent is
// within its first 3 days, 300s afterward, and 300s when created_at is unknown.
func feedPollRampForCreatedAt(createdAtMs, nowMs int64) int32 {
	return feedpoll.EffectiveInterval(feedPollRampSteadySec, false, createdAtMs, nowMs)
}

// GetMySettings returns the agent's runtime settings, authenticated via the
// agent access token (not a console session). The agent polls this to sync its
// local config.json with the backend, which is the source of truth. updated_at
// lets the caller resolve which side is newer. feed_poll_interval reflects the
// onboarding ramp until the user explicitly overrides it (see feedPollRampSec).
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
	feedPollInterval := settings.FeedPollInterval
	if !settings.FeedPollIntervalUserSet {
		feedPollInterval = feedPollRampSec(ctx, agentID, settings)
	}
	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"recurring_publish":        settings.RecurringPublish,
		"feed_poll_interval":       feedPollInterval,
		"auto_reply_pm":            settings.AutoReplyPM,
		"auto_comment":             settings.AutoComment,
		"show_add_friend":          settings.ShowAddFriend,
		"feed_delivery_preference": settings.FeedDeliveryPreference,
		"mode":                     settings.Mode,
		"runtime_name":             settings.RuntimeName,
		"runtime_version":          settings.RuntimeVersion,
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
		// FeedPollIntervalUserSet must be sent explicitly to pin feed_poll_interval
		// as a user override; a value without this flag updates the stored cadence
		// but leaves the onboarding ramp in effect (so a client echoing its default
		// can never silently disable the ramp).
		FeedPollIntervalUserSet *bool `json:"feed_poll_interval_user_set"`
		AutoReplyPM             *bool `json:"auto_reply_pm"`
		AutoComment             *bool `json:"auto_comment"`
		ShowAddFriend           *bool `json:"show_add_friend"`
		OfficialPMOptout        *bool `json:"official_pm_optout"`
	}
	raw, _ := c.Body()
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(c, http.StatusBadRequest, 400, "invalid body", nil)
		return
	}
	if body.FeedPollInterval != nil && !consoledal.FeedPollIntervalInRange(*body.FeedPollInterval) {
		writeJSON(c, http.StatusBadRequest, 400, "feed_poll_interval must be within [10, 86400] seconds", nil)
		return
	}
	clientInfo := reqinfo.ClientFromContext(ctx)
	model := clientInfo.Model
	identity, hasIdentity := runtimeidentity.Parse(clientInfo.Host)
	if err := db.DB.Transaction(func(tx *gorm.DB) error {
		if err := consoledal.UpdateAgentReported(tx, agentID, body.FeedDeliveryPreference, body.Mode, body.RecurringPublish, body.FeedPollInterval, body.FeedPollIntervalUserSet, body.AutoReplyPM, body.OfficialPMOptout, body.AutoComment, body.ShowAddFriend); err != nil {
			return err
		}
		// Persist X-Client-Model in the same transaction. The CLI records its
		// local "reported" snapshot only after this endpoint succeeds, so a
		// model failure must roll the settings write back and remain retryable.
		if err := consoledal.UpdateAgentModel(tx, agentID, model); err != nil {
			return err
		}
		if hasIdentity {
			return consoledal.UpdateRuntimeIdentity(tx, agentID, identity.Name, identity.Version)
		}
		return nil
	}); err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	agentcard.PublishRebuild(ctx, agentID, "settings_update")
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

	// Beat names are the agent's profile keywords. We keep the lowercased form
	// for display but match on the separator-normalized form, so a hyphenated
	// beat ("ai-agents") lines up with the item tagger's spaced form
	// ("ai agents"). Dedup by the normalized key so convention-variant
	// duplicates collapse into one beat.
	type beatName struct{ display, norm string }
	beatList := make([]beatName, 0, len(resp.Agent.Keywords))
	seen := make(map[string]bool, len(resp.Agent.Keywords))
	for _, kw := range resp.Agent.Keywords {
		display := strings.TrimSpace(strings.ToLower(kw))
		norm := tagnorm.Normalize(kw)
		if norm == "" || seen[norm] {
			continue
		}
		seen[norm] = true
		beatList = append(beatList, beatName{display: display, norm: norm})
	}
	if len(beatList) == 0 {
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
	// CountBeatMatches normalizes internally; pass the normalized beat as the
	// map key so lookups below are unambiguous.
	normNames := make([]string, len(beatList))
	for i, b := range beatList {
		normNames[i] = b.norm
	}
	pushed := consoledal.CountBeatMatches(deliveredRows, normNames)
	kept := consoledal.CountBeatMatches(keptRows, normNames)

	// signalAgg.Counts is keyed by the readable tag (it also feeds trending DMs).
	// Fold it onto normalized keys so a beat matches every separator variant.
	normSignals := make(map[string]int64, len(signalAgg.Counts))
	for tag, n := range signalAgg.Counts {
		normSignals[tagnorm.Normalize(tag)] += n
	}

	var maxSignals int64
	for _, b := range beatList {
		if s := normSignals[b.norm]; s > maxSignals {
			maxSignals = s
		}
	}

	beats := make([]map[string]interface{}, 0, len(beatList))
	for _, b := range beatList {
		signals := normSignals[b.norm]
		beats = append(beats, map[string]interface{}{
			// key/name stay human-readable (the profile keyword); b.norm is an
			// internal match key and is deliberately not exposed.
			"key":     b.display,
			"name":    b.display,
			"tier":    beatTier(signals, maxSignals),
			"signals": signals,
			"pushed":  pushed[b.norm],
			"kept":    kept[b.norm],
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
		AutoReplyPM   *bool   `json:"auto_reply_pm"`
		AutoComment   *bool   `json:"auto_comment"`
		ShowAddFriend *bool   `json:"show_add_friend"`
		Lang          *string `json:"lang"`
	}
	_ = json.Unmarshal(body, &extra)
	if extra.Lang != nil && *extra.Lang != "" && *extra.Lang != "zh" && *extra.Lang != "en" {
		writeJSON(c, http.StatusBadRequest, 400, "lang must be one of \"\", \"zh\", \"en\"", nil)
		return
	}
	if req.FeedPollInterval != nil && !consoledal.FeedPollIntervalInRange(*req.FeedPollInterval) {
		writeJSON(c, http.StatusBadRequest, 400, "feed_poll_interval must be within [10, 86400] seconds", nil)
		return
	}
	if req.RecurringPublish != nil {
		current.RecurringPublish = *req.RecurringPublish
	}
	if req.FeedPollInterval != nil {
		current.FeedPollInterval = *req.FeedPollInterval
		// Explicit console edit is a user override: stop the onboarding ramp.
		current.FeedPollIntervalUserSet = true
	}
	if extra.AutoReplyPM != nil {
		current.AutoReplyPM = *extra.AutoReplyPM
	}
	if extra.AutoComment != nil {
		current.AutoComment = *extra.AutoComment
	}
	if extra.ShowAddFriend != nil {
		current.ShowAddFriend = *extra.ShowAddFriend
	}
	if extra.Lang != nil {
		current.Lang = *extra.Lang
	}

	if err := consoledal.UpsertSettings(db.DB, current); err != nil {
		writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	agentcard.PublishRebuild(ctx, agentID, "settings_update")

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

	// Store in Redis: console:code:{code} = {agent_id}:{access_token}.
	redisKey := "console:code:" + code
	redisVal := fmt.Sprintf("%d:%s", agentID, accessToken)
	if err := mq.RDB.Set(ctx, redisKey, redisVal, legacyConsoleHandoffTTL).Err(); err != nil {
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

// PushFeedEvents .
// @router /api/v1/items/events [POST]
// PushFeedEvents ingests follow-up behavior events for ranking labels.
// @Summary Push follow-up behavior events
// @Description Report per-item agent behavior (surface/question/discussion/task) as training labels
// @Tags Item
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body PushFeedEventsReq true "Feed events batch"
// @Success 200 {object} PushFeedEventsResp
// @Failure 401 {object} BaseResp
// @Router /api/v1/items/events [post]
func PushFeedEvents(ctx context.Context, c *app.RequestContext) {
	var req apimodel.PushFeedEventsReq
	if !bindOrBadRequest(c, &req) {
		return
	}
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}

	validKinds := map[string]bool{"surface": true, "question": true, "discussion": true, "task": true}
	events := make([]followuplog.Event, 0, len(req.Events))
	skipped := make([]string, 0)
	now := time.Now().UnixMilli()

	for _, it := range req.Events {
		if !validKinds[it.Kind] {
			skipped = append(skipped, "invalid kind "+it.Kind)
			continue
		}
		itemID, err := strconv.ParseInt(strings.TrimSpace(it.ItemID), 10, 64)
		if err != nil {
			skipped = append(skipped, "invalid item_id "+it.ItemID)
			continue
		}
		dedupKey := ""
		if it.DedupKey != nil {
			dedupKey = strings.TrimSpace(*it.DedupKey)
		}
		if dedupKey == "" {
			skipped = append(skipped, "missing dedup_key for item "+it.ItemID)
			continue
		}
		reportedAt := now
		if it.Ts != nil && *it.Ts > 0 {
			reportedAt = *it.Ts
		}
		events = append(events, followuplog.Event{
			AgentID:      agentID,
			ItemID:       itemID,
			Kind:         it.Kind,
			ImpressionID: optStr(it.ImpressionID),
			Brief:        optStr(it.Brief),
			SessionKey:   optStr(it.SessionKey),
			Channel:      optStr(it.Channel),
			ServerID:     optStr(it.ServerID),
			DedupKey:     dedupKey,
			ReportedAt:   reportedAt,
		})
	}

	if len(events) > 0 {
		if err := followuplog.Publish(ctx, events); err != nil {
			writeJSON(c, http.StatusInternalServerError, 500, err.Error(), nil)
			return
		}
	}

	data := map[string]interface{}{
		"accepted": len(events),
		"skipped":  len(skipped),
	}
	if len(skipped) > 0 {
		data["skipped_reasons"] = skipped
	}
	logger.Ctx(ctx).Info("PushFeedEvents", "agentID", agentID, "accepted", len(events), "skipped", len(skipped))
	writeJSON(c, http.StatusOK, 0, "success", data)
}

func optStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
