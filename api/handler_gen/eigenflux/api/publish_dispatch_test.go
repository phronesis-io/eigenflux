package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"eigenflux_server/api/clients"
	"eigenflux_server/kitex_gen/eigenflux/base"
	itemrpc "eigenflux_server/kitex_gen/eigenflux/item"
	"eigenflux_server/kitex_gen/eigenflux/item/itemservice"
	"eigenflux_server/pkg/itemdispatch"
	"eigenflux_server/pkg/mq"
	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type durablePublishClient struct {
	itemservice.Client
	requested bool
}

func (c *durablePublishClient) PublishItem(ctx context.Context, req *itemrpc.PublishItemReq, opts ...callopt.Option) (*itemrpc.PublishItemResp, error) {
	c.requested = itemdispatch.DurableDispatchRequested(ctx)
	return &itemrpc.PublishItemResp{ItemId: 201, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func TestPublishTransfersDispatchOwnershipToRPC(t *testing.T) {
	client := &durablePublishClient{}
	oldClient, oldRedis := clients.ItemClient, mq.RDB
	clients.ItemClient = client
	server := miniredis.RunT(t)
	mq.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = mq.RDB.Close(); clients.ItemClient = oldClient; mq.RDB = oldRedis })
	c := app.NewContext(0)
	c.Request.Header.SetMethod(http.MethodPost)
	c.Request.Header.SetContentTypeBytes([]byte("application/json"))
	c.Request.SetBodyString(`{"content":"A concrete official release."}`)
	c.Request.Header.SetContentLength(len(c.Request.Body()))
	c.Set("agent_id", int64(101))
	Publish(context.Background(), c)
	require.Equal(t, http.StatusOK, c.Response.StatusCode(), "body: %s", c.Response.Body())
	require.True(t, client.requested, "body: %s", c.Response.Body())
	var response struct {
		Code int
		Data struct {
			ItemID string `json:"item_id"`
		}
	}
	require.NoError(t, json.Unmarshal(c.Response.Body(), &response))
	require.Zero(t, response.Code)
	require.Equal(t, "201", response.Data.ItemID)
	require.Zero(t, mq.RDB.XLen(context.Background(), "stream:item:publish").Val(), "gateway must not create a second delivery")
}
