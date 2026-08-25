package consolev2

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	notificationrpc "eigenflux_server/kitex_gen/eigenflux/notification"
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
	case "system", "milestone", "trade":
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
	response, err := s.notificationClient.ListPending(ctx, &notificationrpc.ListPendingReq{AgentId: agentIDValue})
	if err != nil || response == nil || response.BaseResp == nil || response.BaseResp.Code != 0 {
		fail(c, http.StatusServiceUnavailable, "NOTIFICATIONS_UNAVAILABLE", "V2 notifications are temporarily unavailable", nil)
		return
	}
	limit := len(response.Notifications)
	if limit > 50 {
		limit = 50
	}
	notifications := make([]map[string]interface{}, 0, limit)
	for _, notification := range response.Notifications[:limit] {
		if notification == nil {
			continue
		}
		content, truncated := truncateRunes(notification.Content, 2000)
		notifications = append(notifications, map[string]interface{}{
			"notification_id": fmt.Sprintf("%d", notification.NotificationId),
			"source_ref":      map[string]interface{}{"type": notification.SourceType, "id": fmt.Sprintf("%d", notification.NotificationId)},
			"type":            notification.Type, "source_type": notification.SourceType,
			"content": content, "content_truncated": truncated, "created_at": notification.CreatedAt,
			"issuer_identity": notificationIssuerIdentity(notification), "action_authority": "none",
		})
	}
	reply(c, http.StatusOK, map[string]interface{}{
		"notifications": notifications, "has_more": len(response.Notifications) > limit,
	})
}

type ackNotificationRequest struct {
	Notifications []struct {
		NotificationID int64  `json:"notification_id"`
		SourceType     string `json:"source_type"`
	} `json:"notifications"`
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
	seen := make(map[int64]struct{}, len(request.Notifications))
	for _, notification := range request.Notifications {
		sourceType := strings.TrimSpace(notification.SourceType)
		if notification.NotificationID <= 0 || sourceType == "" || len(sourceType) > 64 {
			fail(c, http.StatusBadRequest, "INVALID_NOTIFICATION_ACK", "notification acknowledgement is invalid", nil)
			return
		}
		if _, exists := seen[notification.NotificationID]; exists {
			continue
		}
		seen[notification.NotificationID] = struct{}{}
		items = append(items, &notificationrpc.AckNotificationItem{
			NotificationId: notification.NotificationID, SourceType: sourceType,
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
