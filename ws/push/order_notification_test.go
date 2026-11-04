package push

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/kitex/client/callopt"

	"eigenflux_server/kitex_gen/eigenflux/base"
	notificationrpc "eigenflux_server/kitex_gen/eigenflux/notification"
)

type notificationClientStub struct {
	listAgentID int64
	ackCalls    int
	response    *notificationrpc.ListPendingResp
}

func (s *notificationClientStub) ListPending(_ context.Context, req *notificationrpc.ListPendingReq, _ ...callopt.Option) (*notificationrpc.ListPendingResp, error) {
	s.listAgentID = req.AgentId
	return s.response, nil
}

func (s *notificationClientStub) AckNotifications(context.Context, *notificationrpc.AckNotificationsReq, ...callopt.Option) (*notificationrpc.AckNotificationsResp, error) {
	s.ackCalls++
	return &notificationrpc.AckNotificationsResp{BaseResp: &base.BaseResp{}}, nil
}

func TestFetchPendingNotificationDataIsAgentScopedAndDoesNotAck(t *testing.T) {
	payload := `{"event_id":9007199254740993,"order_id":9007199254740995,"recipient_agent_id":42,"actor_agent_id":0,"snapshot_id":9007199254740997,"order_version":2}`
	badPayload := `{}`
	stub := &notificationClientStub{response: &notificationrpc.ListPendingResp{
		BaseResp: &base.BaseResp{}, HasMore: boolPointer(true), NextCursor: stringPointer("cursor-2"),
		Notifications: []*notificationrpc.PendingNotification{
			{NotificationId: 81, SourceType: "commission_order", Type: "order.state.changed.v1", PayloadJson: &payload},
			{NotificationId: 82, SourceType: "system", Type: "system", PayloadJson: &badPayload},
		},
	}}
	data, err := fetchPendingNotificationData(context.Background(), stub, 42)
	if err != nil {
		t.Fatal(err)
	}
	if stub.listAgentID != 42 || stub.ackCalls != 0 {
		t.Fatalf("ListPending agent=%d ack calls=%d", stub.listAgentID, stub.ackCalls)
	}
	if len(data.Notifications) != 1 || !data.HasMore || data.NextCursor != "cursor-2" {
		t.Fatalf("unexpected notification page: %#v", data)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(data.Notifications[0].Payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["order_id"] != "9007199254740995" || data.Notifications[0].NotificationID != "81" {
		t.Fatalf("external IDs lost precision: %#v", data.Notifications[0])
	}
}

func boolPointer(value bool) *bool       { return &value }
func stringPointer(value string) *string { return &value }

func TestFetchPendingModerationAndOrderNotifications(t *testing.T) {
	moderation := `{"notification_version":2,"moderation_id":"9007199254740993","commission_id":"9007199254740995","recipient_agent_id":"42","draft_version":2,"status":"review","decision":"review","flag":"","field":"title","reason":"Clarify title","occurred_at":1000}`
	order := `{"order_id":9007199254740997}`
	foreign := strings.Replace(moderation, `"recipient_agent_id":"42"`, `"recipient_agent_id":"43"`, 1)
	invalid := `{}`
	stub := &notificationClientStub{response: &notificationrpc.ListPendingResp{BaseResp: &base.BaseResp{}, Notifications: []*notificationrpc.PendingNotification{
		{NotificationId: 81, SourceType: "commission_order", PayloadJson: &order},
		{NotificationId: 82, SourceType: "commission_moderation", PayloadJson: &moderation},
		{NotificationId: 83, SourceType: "commission_moderation", PayloadJson: &foreign},
		{NotificationId: 84, SourceType: "commission_moderation", PayloadJson: &invalid},
		{NotificationId: 85, SourceType: "commission_moderation"},
		{NotificationId: 86, SourceType: "commission_moderation", PayloadJson: stringPointer(strings.Replace(moderation, `"status":"review"`, `"status":"future"`, 1))},
	}}}
	data, err := fetchPendingNotificationData(context.Background(), stub, 42)
	if err != nil || len(data.Notifications) != 2 || stub.ackCalls != 0 {
		t.Fatalf("push projection: %#v %v", data, err)
	}
	notice := data.Notifications[1]
	if notice.SourceType != "commission_moderation" || string(notice.Payload) != moderation {
		t.Fatal("moderation payload lost string IDs or fields")
	}
}
