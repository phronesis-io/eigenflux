package api

import (
	"context"
	"eigenflux_server/api/clients"
	"eigenflux_server/kitex_gen/eigenflux/base"
	notificationrpc "eigenflux_server/kitex_gen/eigenflux/notification"
	"eigenflux_server/kitex_gen/eigenflux/notification/notificationservice"
	"github.com/cloudwego/kitex/client/callopt"
	"testing"
)

type moderationNotificationClient struct {
	notificationservice.Client
	response *notificationrpc.ListPendingResp
}

func (c moderationNotificationClient) ListPending(context.Context, *notificationrpc.ListPendingReq, ...callopt.Option) (*notificationrpc.ListPendingResp, error) {
	return c.response, nil
}
func (c moderationNotificationClient) AckNotifications(context.Context, *notificationrpc.AckNotificationsReq, ...callopt.Option) (*notificationrpc.AckNotificationsResp, error) {
	panic("legacy feed must not acknowledge moderation")
}

func TestLegacyFeedModerationProjectionAndExplicitAck(t *testing.T) {
	original := clients.NotificationClient
	defer func() { clients.NotificationClient = original }()
	raw := `{"notification_version":2,"moderation_id":"9007199254740993","commission_id":"9007199254740995","recipient_agent_id":"1","draft_version":2,"status":"pending","decision":"","flag":"","field":"","reason":"","occurred_at":1000}`
	clients.NotificationClient = moderationNotificationClient{response: &notificationrpc.ListPendingResp{BaseResp: &base.BaseResp{}, Notifications: []*notificationrpc.PendingNotification{{NotificationId: 9007199254740997, SourceType: "commission_moderation", PayloadJson: &raw}}}}
	pending, projected := fetchPendingNotifications(context.Background(), 1)
	if len(projected) != 1 {
		t.Fatal("moderation notice missing")
	}
	ackNotifications(1, pending)
	_, foreign := fetchPendingNotifications(context.Background(), 2)
	if len(foreign) != 0 {
		t.Fatal("cross-recipient payload exposed")
	}
}
