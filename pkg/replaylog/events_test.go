package replaylog

import (
	"context"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/reqinfo"
	"encoding/json"
	"github.com/alicebob/miniredis/v2"
	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/redis/go-redis/v9"
	"testing"
)

func TestTypedIdentityValidation(t *testing.T) {
	for _, k := range []string{"broadcast", "commission", "agent"} {
		s := ServedItem{PipelineVersion: "need_search_v1", SampleSchemaVersion: 2, RequestMode: "search", SourceKind: k, SourceID: 7, ContextID: 3}
		if k == "broadcast" {
			s.ItemID = 7
		}
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
		s.ItemID = 9
		if err := s.Validate(); err == nil {
			t.Fatal("invalid typed identity accepted")
		}
	}
	if err := (ServedItem{ItemID: 7}).Validate(); err != nil {
		t.Fatal("old event rejected", err)
	}
}

func TestPublishUsesDeliveryVersionInsteadOfCachedRankingVersion(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	prior := mq.RDB
	mq.RDB = rdb
	defer func() { mq.RDB = prior }()
	for _, version := range []string{"1.0.28", ""} {
		ctx := metainfo.WithPersistentValue(context.Background(), reqinfo.KeyCLIVer, version)
		if err := Publish(ctx, "imp", 1, `{"context":{"cli_ver":"old","client_os":"linux"},"keywords":["ai"]}`, []ServedItem{{ItemID: 2}}); err != nil {
			t.Fatal(err)
		}
		msgs, err := rdb.XRevRangeN(ctx, StreamName, "+", "-", 1).Result()
		if err != nil {
			t.Fatal(err)
		}
		var features struct {
			Context  map[string]string `json:"context"`
			Keywords []string          `json:"keywords"`
		}
		if err = json.Unmarshal([]byte(msgs[0].Values["agent_features"].(string)), &features); err != nil {
			t.Fatal(err)
		}
		if features.Context["cli_ver"] != version || features.Context["client_os"] != "linux" || len(features.Keywords) != 1 {
			t.Fatal(features)
		}
	}
}
