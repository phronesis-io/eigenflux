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
