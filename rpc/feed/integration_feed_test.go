package main

import (
	"context"
	"testing"

	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/feed"
	sortapi "eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/kitex_gen/eigenflux/sort/sortservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/feedcache"
	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/redis/go-redis/v9"
)

type managedSortStub struct {
	sortservice.Client
	version int64
}

func (s managedSortStub) SortItems(_ context.Context, req *sortapi.SortItemsReq, _ ...callopt.Option) (*sortapi.SortItemsResp, error) {
	if req.ManagedMatchingVersion == nil || *req.ManagedMatchingVersion != s.version {
		return &sortapi.SortItemsResp{BaseResp: &base.BaseResp{Code: 409, Msg: "managed_matching_changed"}}, nil
	}
	return &sortapi.SortItemsResp{ItemIds: []int64{}, ManagedMatchingVersion: &s.version, BaseResp: &base.BaseResp{}}, nil
}

func TestManagedFeedPreservesMatchingVersionOnEmptyResult(t *testing.T) {
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	defer client.Close()
	previous := sortClient
	sortClient = managedSortStub{version: 7}
	defer func() { sortClient = previous }()
	// Personal Discovery may be enabled, but enterprise broadcasts must use
	// the managed version through ranking without inventing onboarding state.
	s := &FeedServiceImpl{config: &config.Config{EnableSDKIntegrations: true, EnableNeedSearch: true}, feedCache: feedcache.NewFeedCache(client), impressionIDGen: &stubIDGen{ids: []int64{1, 2}}}
	version := int64(7)
	response, err := s.FetchFeed(context.Background(), &feed.FetchFeedReq{AgentId: 1, ManagedMatchingVersion: &version})
	if err != nil || response.BaseResp.Code != 0 || response.ManagedMatchingVersion == nil || *response.ManagedMatchingVersion != 7 || len(response.Items) != 0 {
		t.Fatal(response, err)
	}
	version = 8
	response, err = s.FetchFeed(context.Background(), &feed.FetchFeedReq{AgentId: 1, ManagedMatchingVersion: &version})
	if err != nil || response.BaseResp.Code != 409 {
		t.Fatal(response, err)
	}
}
