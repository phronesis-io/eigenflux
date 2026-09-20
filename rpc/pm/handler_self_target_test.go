package main

import (
	"context"
	"testing"

	"eigenflux_server/kitex_gen/eigenflux/pm"
)

// A bare service has no Redis, PostgreSQL, or send guard. Each self-target
// guard must reject before touching any of them, so reaching a dependency
// fails the test instead of silently passing.
func TestSendFriendRequestRejectsSelfTargetBeforeRedis(t *testing.T) {
	service := &PMServiceImpl{}
	resp, err := service.SendFriendRequest(context.Background(), &pm.SendFriendRequestReq{FromUid: 7, ToUid: 7})
	if err != nil {
		t.Fatalf("SendFriendRequest error: %v", err)
	}
	if resp.BaseResp.Code != 400 || resp.BaseResp.Msg != "cannot send a friend request to yourself" {
		t.Fatalf("response = %+v, want code 400 self-target rejection", resp.BaseResp)
	}
	if resp.RequestId != 0 {
		t.Fatalf("request_id = %d, want none", resp.RequestId)
	}
}

func TestBlockUserRejectsSelfTargetBeforeDatabase(t *testing.T) {
	service := &PMServiceImpl{}
	resp, err := service.BlockUser(context.Background(), &pm.BlockUserReq{FromUid: 7, ToUid: 7})
	if err != nil {
		t.Fatalf("BlockUser error: %v", err)
	}
	if resp.BaseResp.Code != 400 || resp.BaseResp.Msg != "cannot block yourself" {
		t.Fatalf("response = %+v, want code 400 self-target rejection", resp.BaseResp)
	}
}

func TestSendPMRejectsSelfReceiverBeforeSendGuard(t *testing.T) {
	service := &PMServiceImpl{}
	resp, err := service.SendPM(context.Background(), &pm.SendPMReq{SenderId: 7, ReceiverId: 7, Content: "hello me"})
	if err != nil {
		t.Fatalf("SendPM error: %v", err)
	}
	if resp.BaseResp.Code != 400 || resp.BaseResp.Msg != "cannot send a private message to yourself" {
		t.Fatalf("response = %+v, want code 400 self-target rejection", resp.BaseResp)
	}
	if resp.MsgId != 0 || resp.ConvId != 0 {
		t.Fatalf("ids = (%d, %d), want none", resp.MsgId, resp.ConvId)
	}
}
