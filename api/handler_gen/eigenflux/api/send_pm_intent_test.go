package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"eigenflux_server/api/clients"
	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/pm"
	"eigenflux_server/kitex_gen/eigenflux/pm/pmservice"
	"eigenflux_server/pkg/activity"
	"eigenflux_server/pkg/mq"
	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/redis/go-redis/v9"
)

func pmIntentContext(body string) *app.RequestContext {
	c := app.NewContext(0)
	c.Request.Header.SetMethod(http.MethodPost)
	c.Request.Header.SetContentTypeBytes([]byte("application/json"))
	c.Request.SetBodyString(body)
	c.Request.Header.SetContentLength(len(c.Request.Body()))
	c.Set("agent_id", int64(1))
	return c
}

func TestSendPMForwardsExplicitReply(t *testing.T) {
	client := &requestCapturingPMClient{}
	old := clients.PMClient
	clients.PMClient = client
	t.Cleanup(func() { clients.PMClient = old })
	c := pmIntentContext(`{"conv_id":"2","content":"answer","message_kind":"reply","reply_to_message_id":"3"}`)
	SendPM(context.Background(), c)
	if client.req == nil || client.req.GetMessageKind() != "reply" || client.req.GetReplyToMessageId() != 3 {
		t.Fatalf("reply metadata missing: %+v", client.req)
	}
	for _, id := range []string{"0", "-1", "not-an-id"} {
		client.req = nil
		c = pmIntentContext(`{"conv_id":"2","content":"answer","message_kind":"reply","reply_to_message_id":"` + id + `"}`)
		SendPM(context.Background(), c)
		if c.Response.StatusCode() != http.StatusBadRequest || client.req != nil {
			t.Fatalf("invalid reply target reached RPC: %s", c.Response.Body())
		}
	}
}

type intentReceiptPMClient struct {
	pmservice.Client
	msgID int64
}

func (c *intentReceiptPMClient) SendPM(context.Context, *pm.SendPMReq, ...callopt.Option) (*pm.SendPMResp, error) {
	return &pm.SendPMResp{MsgId: c.msgID, ConvId: c.msgID, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func TestSendPMBlockedReceiptDoesNotEmitSuccessfulActivity(t *testing.T) {
	redisClient := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	oldClient, oldMQ := clients.PMClient, mq.RDB
	clients.PMClient = &intentReceiptPMClient{}
	mq.RDB = redisClient
	t.Cleanup(func() { clients.PMClient = oldClient; mq.RDB = oldMQ; _ = redisClient.Close() })
	c := pmIntentContext(`{"receiver_id":"2","content":"hello"}`)
	SendPM(context.Background(), c)
	if c.Response.StatusCode() != http.StatusOK {
		t.Fatalf("blocked response contract changed: %s", c.Response.Body())
	}
	time.Sleep(50 * time.Millisecond)
	if n, err := redisClient.XLen(context.Background(), activity.StreamName).Result(); err != nil || n != 0 {
		t.Fatalf("blocked send counted as activity: %d %v", n, err)
	}
	clients.PMClient = &intentReceiptPMClient{msgID: 10}
	SendPM(context.Background(), pmIntentContext(`{"receiver_id":"2","content":"hello"}`))
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if n, _ := redisClient.XLen(context.Background(), activity.StreamName).Result(); n == 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("persisted send did not emit activity")
}
