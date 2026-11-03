package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"eigenflux_server/api/clients"
	"eigenflux_server/kitex_gen/eigenflux/base"
	feedrpc "eigenflux_server/kitex_gen/eigenflux/feed"
	"eigenflux_server/kitex_gen/eigenflux/feed/feedservice"
	pmrpc "eigenflux_server/kitex_gen/eigenflux/pm"
	"eigenflux_server/kitex_gen/eigenflux/pm/pmservice"
	"eigenflux_server/pkg/reqinfo"
	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/kitex/client/callopt"
)

type maintenanceEmptyFeed struct{ feedservice.Client }

func (*maintenanceEmptyFeed) FetchFeed(context.Context, *feedrpc.FetchFeedReq, ...callopt.Option) (*feedrpc.FetchFeedResp, error) {
	return &feedrpc.FetchFeedResp{BaseResp: &base.BaseResp{}}, nil
}

type maintenanceEmptyPM struct{ pmservice.Client }

func (*maintenanceEmptyPM) FetchPM(context.Context, *pmrpc.FetchPMReq, ...callopt.Option) (*pmrpc.FetchPMResp, error) {
	return &pmrpc.FetchPMResp{BaseResp: &base.BaseResp{}, NextCursor: 99}, nil
}

func TestLegacyMaintenanceDeliveredOnEmptyReads(t *testing.T) {
	oldFeed, oldPM := clients.FeedClient, clients.PMClient
	clients.FeedClient, clients.PMClient = &maintenanceEmptyFeed{}, &maintenanceEmptyPM{}
	t.Cleanup(func() { clients.FeedClient, clients.PMClient = oldFeed, oldPM })
	for _, tc := range []struct {
		name, path string
		handler    app.HandlerFunc
	}{
		{"feed", "/api/v1/items/feed?action=more", Feed},
		{"pm_v1", "/api/v1/pm/fetch", FetchPM},
		{"pm_v2", "/api/v2/pm/fetch", FetchPM},
	} {
		for _, version := range []string{"0.0.33", "1.0.7", ""} {
			t.Run(tc.name+"/"+version, func(t *testing.T) {
				c := app.NewContext(0)
				c.Request.Header.SetMethod(http.MethodGet)
				c.Request.SetRequestURI(tc.path)
				c.Set("agent_id", int64(42))
				ctx := metainfo.WithPersistentValue(context.Background(), reqinfo.KeyCLIVer, version)
				tc.handler(ctx, c)
				if c.Response.StatusCode() != http.StatusOK {
					t.Fatalf("status=%d body=%s", c.Response.StatusCode(), c.Response.Body())
				}
				var body struct {
					Code int
					Data map[string]interface{}
				}
				if err := json.Unmarshal(c.Response.Body(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Code != 0 || (body.Data["client_maintenance"] != nil) != (version == "0.0.33") {
					t.Fatalf("response=%s", c.Response.Body())
				}
				if tc.name != "feed" && body.Data["next_cursor"] != "99" {
					t.Fatalf("cursor changed: %#v", body.Data)
				}
			})
		}
	}
}
