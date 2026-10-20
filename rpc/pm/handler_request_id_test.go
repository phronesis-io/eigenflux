package main

import (
	"context"
	"strings"
	"testing"

	"eigenflux_server/kitex_gen/eigenflux/pm"
	"eigenflux_server/rpc/pm/sendguard"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestSendPMUsesRequestIdentityThroughThrift(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	guard := sendguard.New(rdb)
	service := &PMServiceImpl{sendGuard: guard}
	ctx := context.Background()
	convID := int64(2)
	for i, requestID := range []string{"", "pm-reply:100", "pm-reply:101"} {
		key := sendguard.RequestFingerprint(1, "conversation", convID, "收到", requestID)
		lease, _, err := guard.Acquire(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if err := guard.Complete(ctx, lease, sendguard.Result{MsgID: int64(10 + i), ConvID: convID}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		id   string
		want int64
	}{{"pm-reply:100", 11}, {"pm-reply:101", 12}, {"pm-reply:100", 11}, {"", 10}} {
		req := &pm.SendPMReq{SenderId: 1, Content: "收到", ConvId: &convID}
		if tc.id != "" {
			req.RequestId = &tc.id
		}
		wire := make([]byte, req.BLength())
		req.FastWrite(wire)
		var decoded pm.SendPMReq
		if _, err := decoded.FastRead(wire); err != nil {
			t.Fatal(err)
		}
		resp, err := service.SendPM(ctx, &decoded)
		if err != nil || resp.BaseResp.Code != 0 || resp.MsgId != tc.want || resp.ConvId != convID {
			t.Fatalf("request %q returned wrong receipt: %+v, %v", tc.id, resp, err)
		}
	}
}

func TestSendPMRejectsOversizedRequestIDBeforeRedis(t *testing.T) {
	id := strings.Repeat("a", 129)
	resp, err := (&PMServiceImpl{}).SendPM(context.Background(), &pm.SendPMReq{SenderId: 1, ReceiverId: 2, Content: "ok", RequestId: &id})
	if err != nil || resp.BaseResp.Code != 400 || !strings.Contains(resp.BaseResp.Msg, "request_id") {
		t.Fatalf("oversized request ID not rejected: %+v, %v", resp, err)
	}
}
