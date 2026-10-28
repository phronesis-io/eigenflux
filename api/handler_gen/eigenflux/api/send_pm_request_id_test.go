package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"eigenflux_server/api/clients"
	"eigenflux_server/kitex_gen/eigenflux/pm"
	"eigenflux_server/kitex_gen/eigenflux/pm/pmservice"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/kitex/client/callopt"
)

type requestCapturingPMClient struct {
	pmservice.Client
	req *pm.SendPMReq
}

func (c *requestCapturingPMClient) SendPM(_ context.Context, req *pm.SendPMReq, _ ...callopt.Option) (*pm.SendPMResp, error) {
	c.req = req
	return nil, errors.New("stop after capturing RPC request")
}

func TestSendPMForwardsRequestIdentity(t *testing.T) {
	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"conv_id":"2","content":"收到","request_id":"pm-reply:100"}`, "pm-reply:100"},
		{`{"conv_id":"2","content":"收到","request_id":"pm-reply:101"}`, "pm-reply:101"},
		{`{"conv_id":"2","content":"收到"}`, ""},
	} {
		t.Run(tc.want, func(t *testing.T) {
			client := &requestCapturingPMClient{}
			old := clients.PMClient
			clients.PMClient = client
			t.Cleanup(func() { clients.PMClient = old })
			c := app.NewContext(0)
			c.Request.Header.SetMethod(http.MethodPost)
			c.Request.Header.SetContentTypeBytes([]byte("application/json"))
			c.Request.SetBodyString(tc.body)
			c.Request.Header.SetContentLength(len(c.Request.Body()))
			c.Set("agent_id", int64(1))
			SendPM(context.Background(), c)
			if c.Response.StatusCode() != http.StatusInternalServerError || client.req == nil {
				t.Fatalf("request did not reach RPC: %s", c.Response.Body())
			}
			if client.req.SenderId != 1 || client.req.GetConvId() != 2 || client.req.Content != "收到" || client.req.GetRequestId() != tc.want {
				t.Fatalf("request identity lost at gateway: %+v", client.req)
			}
		})
	}
}
