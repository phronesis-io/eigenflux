package main

import (
	"context"
	"strings"
	"testing"

	"eigenflux_server/kitex_gen/eigenflux/pm"
)

func TestSendPMRejectsInvalidMessageEncodingBeforeRedis(t *testing.T) {
	service := &PMServiceImpl{}
	tests := []struct {
		name       string
		content    string
		wantReason string
	}{
		{name: "invalid UTF-8", content: string([]byte{0xff}), wantReason: "valid UTF-8"},
		{name: "replacement character", content: "损坏�内容", wantReason: "U+FFFD"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := service.SendPM(context.Background(), &pm.SendPMReq{SenderId: 1, ReceiverId: 2, Content: tt.content})
			if err != nil {
				t.Fatalf("SendPM error: %v", err)
			}
			if resp.BaseResp.Code != 400 || !strings.Contains(resp.BaseResp.Msg, tt.wantReason) {
				t.Fatalf("response = %+v, want code 400 containing %q", resp.BaseResp, tt.wantReason)
			}
		})
	}
}

func TestSendFriendRequestRejectsOversizedGreetingBeforeQuota(t *testing.T) {
	service := &PMServiceImpl{}
	greeting := strings.Repeat("猫", 199) + "a🐈!"
	resp, err := service.SendFriendRequest(context.Background(), &pm.SendFriendRequestReq{FromUid: 1, ToUid: 2, Greeting: &greeting})
	if err != nil {
		t.Fatal(err)
	}
	if resp.RequestId != 0 || resp.BaseResp.Code != 400 || !strings.Contains(resp.BaseResp.Msg, "401 weighted characters (maximum 400") {
		t.Fatalf("expected a length error without a request ID, got %+v", resp)
	}
}
