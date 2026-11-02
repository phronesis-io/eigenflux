package consolev2

import (
	"context"
	"eigenflux_server/kitex_gen/eigenflux/base"
	notificationrpc "eigenflux_server/kitex_gen/eigenflux/notification"
	"errors"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/kitex/client/callopt"
	"testing"
)

type pendingFailureClient struct {
	fakeNotificationClient
	response *notificationrpc.ListPendingResp
	err      error
}

func (f *pendingFailureClient) ListPending(context.Context, *notificationrpc.ListPendingReq, ...callopt.Option) (*notificationrpc.ListPendingResp, error) {
	return f.response, f.err
}
func TestPendingNotificationFailureStatus(t *testing.T) {
	for _, tc := range []struct {
		name  string
		code  int32
		err   error
		empty bool
		want  int
	}{
		{name: "invalid_cursor", code: 400, want: 400},
		{name: "durable_store_failure", code: 500, want: 503},
		{name: "transport_failure", err: errors.New("upstream unavailable"), want: 503},
		{name: "empty_response", empty: true, want: 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &pendingFailureClient{err: tc.err}
			if !tc.empty {
				client.response = &notificationrpc.ListPendingResp{BaseResp: &base.BaseResp{Code: tc.code}}
			}
			s := &Service{}
			s.SetNotificationClient(client)
			h := server.Default()
			h.GET("/pending", s.listPendingNotifications)
			status, _, _ := performJSON(t, h, "GET", "/pending", nil)
			if status != tc.want {
				t.Fatalf("status=%d want=%d", status, tc.want)
			}
		})
	}
}
