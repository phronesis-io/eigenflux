package main

import (
	"context"
	"testing"

	search "eigenflux_server/kitex_gen/eigenflux/recordsearch"
	"eigenflux_server/pkg/reqinfo"

	"github.com/bytedance/gopkg/cloud/metainfo"
)

func TestSearchRejectsUntrustedOwnerBeforeDAL(t *testing.T) {
	service := &PMServiceImpl{}

	t.Run("SearchMessages", func(t *testing.T) {
		for _, identity := range []string{"", "2"} {
			ctx := metainfo.WithPersistentValue(context.Background(), reqinfo.KeyAgentID, identity)
			got, err := service.SearchMessages(ctx, &search.SearchReq{OwnerAgentId: 1, Query: "x", Limit: 10})
			if err != nil || got.BaseResp.Code != 403 || len(got.Items) != 0 {
				t.Fatalf("%+v %v", got, err)
			}
		}
		got, err := service.SearchMessages(context.Background(), nil)
		if err != nil || got.BaseResp.Code != 400 {
			t.Fatalf("%+v %v", got, err)
		}
	})

	t.Run("SearchFriends", func(t *testing.T) {
		for _, identity := range []string{"", "2"} {
			ctx := metainfo.WithPersistentValue(context.Background(), reqinfo.KeyAgentID, identity)
			got, err := service.SearchFriends(ctx, &search.SearchReq{OwnerAgentId: 1, Query: "x", Limit: 10})
			if err != nil || got.BaseResp.Code != 403 || len(got.Items) != 0 {
				t.Fatalf("%+v %v", got, err)
			}
		}
		got, err := service.SearchFriends(context.Background(), nil)
		if err != nil || got.BaseResp.Code != 400 {
			t.Fatalf("%+v %v", got, err)
		}
	})
}
