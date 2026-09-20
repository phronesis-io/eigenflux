package consolev2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	notificationrpc "eigenflux_server/kitex_gen/eigenflux/notification"
	"eigenflux_server/pkg/notificationpayload"
)

func platformIssuerIdentity() map[string]interface{} {
	return map[string]interface{}{
		"subject_type": "platform", "subject_id": "eigenflux-platform",
		"display_name": "EigenFlux", "verification_level": "official",
	}
}

func notificationIssuerIdentity(notification *notificationrpc.PendingNotification) map[string]interface{} {
	if notification != nil && notification.PeerShortId != nil && notification.PeerDisplayName != nil {
		subjectID := ""
		if notification.FriendUid != nil {
			subjectID = fmt.Sprintf("%d", *notification.FriendUid)
		}
		return map[string]interface{}{
			"subject_type": "agent", "subject_id": subjectID, "short_id": *notification.PeerShortId,
			"display_name": *notification.PeerDisplayName, "verification_level": "unverified",
		}
	}
	sourceType := ""
	if notification != nil {
		sourceType = notification.SourceType
	}
	switch strings.ToLower(strings.TrimSpace(sourceType)) {
	case "system", "milestone", "trade", "commission_order":
		return platformIssuerIdentity()
	default:
		// Friend requests and unknown legacy notification types are not
		// platform-authored. Their peer identity is resolved by the dedicated
		// V2 communication BFF, so this endpoint must fail closed.
		return nil
	}
}

func (s *Service) listPendingNotifications(ctx context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	if s.notificationClient == nil {
		fail(c, http.StatusServiceUnavailable, "NOTIFICATIONS_UNAVAILABLE", "V2 notifications are temporarily unavailable", nil)
		return
	}
	limit := 50
	if rawLimit := strings.TrimSpace(c.Query("limit")); rawLimit != "" {
		parsed, parseErr := strconv.Atoi(rawLimit)
		if parseErr != nil || parsed <= 0 || parsed > 50 {
			fail(c, http.StatusBadRequest, "INVALID_NOTIFICATION_PAGE", "limit must be between 1 and 50", nil)
			return
		}
		limit = parsed
	}
	limitValue := int32(limit)
	request := &notificationrpc.ListPendingReq{AgentId: agentIDValue, Limit: &limitValue}
	if cursor := strings.TrimSpace(c.Query("cursor")); cursor != "" {
		request.Cursor = &cursor
	}
	response, err := s.notificationClient.ListPending(ctx, request)
	if err != nil || response == nil || response.BaseResp == nil || response.BaseResp.Code != 0 {
		fail(c, http.StatusServiceUnavailable, "NOTIFICATIONS_UNAVAILABLE", "V2 notifications are temporarily unavailable", nil)
		return
	}
	notifications := make([]map[string]interface{}, 0, len(response.Notifications))
	for _, notification := range response.Notifications {
		if notification == nil {
			continue
		}
		content, truncated := truncateRunes(notification.Content, 2000)
		item := map[string]interface{}{
			"notification_id": fmt.Sprintf("%d", notification.NotificationId),
			"source_ref":      map[string]interface{}{"type": notification.SourceType, "id": fmt.Sprintf("%d", notification.NotificationId)},
			"type":            notification.Type, "source_type": notification.SourceType,
			"content": content, "content_truncated": truncated, "created_at": notification.CreatedAt,
			"issuer_identity": notificationIssuerIdentity(notification), "action_authority": "none",
		}
		if notification.PayloadJson != nil && json.Valid([]byte(*notification.PayloadJson)) {
			payload := json.RawMessage(*notification.PayloadJson)
			if notification.SourceType == "commission_order" {
				normalized, normalizeErr := notificationpayload.NormalizeCommissionOrderIDs(*notification.PayloadJson)
				if normalizeErr != nil {
					continue
				}
				payload = normalized
			}
			item["payload"] = payload
		}
		notifications = append(notifications, item)
	}
	reply(c, http.StatusOK, map[string]interface{}{
		"notifications": notifications, "has_more": response.GetHasMore(), "next_cursor": response.GetNextCursor(),
	})
}

type ackNotificationRequest struct {
	Notifications []struct {
		NotificationID json.RawMessage `json:"notification_id"`
		SourceType     string          `json:"source_type"`
	} `json:"notifications"`
}

func parseNotificationID(raw json.RawMessage) (int64, error) {
	text := strings.TrimSpace(string(raw))
	if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
		var decoded string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return 0, err
		}
		text = decoded
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid notification ID")
	}
	return value, nil
}

func (s *Service) ackPendingNotifications(ctx context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	if s.notificationClient == nil {
		fail(c, http.StatusServiceUnavailable, "NOTIFICATIONS_UNAVAILABLE", "V2 notifications are temporarily unavailable", nil)
		return
	}
	var request ackNotificationRequest
	if decodeBody(c, &request) != nil || len(request.Notifications) == 0 || len(request.Notifications) > 50 {
		fail(c, http.StatusBadRequest, "INVALID_NOTIFICATION_ACK", "notifications must contain 1 to 50 items", nil)
		return
	}
	items := make([]*notificationrpc.AckNotificationItem, 0, len(request.Notifications))
	seen := make(map[string]struct{}, len(request.Notifications))
	for _, notification := range request.Notifications {
		sourceType := strings.TrimSpace(notification.SourceType)
		notificationID, parseErr := parseNotificationID(notification.NotificationID)
		if parseErr != nil || sourceType == "" || len(sourceType) > 64 {
			fail(c, http.StatusBadRequest, "INVALID_NOTIFICATION_ACK", "notification acknowledgement is invalid", nil)
			return
		}
		key := sourceType + ":" + strconv.FormatInt(notificationID, 10)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		items = append(items, &notificationrpc.AckNotificationItem{
			NotificationId: notificationID, SourceType: sourceType,
		})
	}
	response, err := s.notificationClient.AckNotifications(ctx, &notificationrpc.AckNotificationsReq{
		AgentId: agentIDValue, Items: items,
	})
	if err != nil || response == nil || response.BaseResp == nil || response.BaseResp.Code != 0 {
		fail(c, http.StatusServiceUnavailable, "NOTIFICATION_ACK_FAILED", "could not acknowledge notifications", nil)
		return
	}
	reply(c, http.StatusOK, map[string]interface{}{"acknowledged": len(items)})
}
