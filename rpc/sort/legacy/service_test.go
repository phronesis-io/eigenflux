package legacy

import (
	"testing"
	"time"

	"eigenflux_server/pkg/config"
	sortDal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/ranker"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestServicesKeepIndependentModels(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	newService := func(path string) *Service {
		s := New(&config.Config{LRRankerEnabled: true, LRRankerModelPath: path, LRRankerReloadInterval: "1h"}, nil, rdb)
		t.Cleanup(s.Close)
		return s
	}
	first := newService("../lrranker/testdata/model.json")
	second := newService("../lrranker/testdata/model_b.json")
	items := map[int64]sortDal.Item{1: {ID: 1, Type: "info", CreatedAt: time.Now()}}
	ranked := []ranker.RankedItem{{ItemID: 1, Score: 0.5}}
	for _, tc := range []struct {
		service *Service
		version string
	}{
		{first, "lr_20260803_1625_e69f47d"},
		{second, "lr_20260731_1454_e69f47d"},
	} {
		got := tc.service.scoreItemsWithLR(ranked, items, nil, &ranker.UserProfile{})
		if got[1].ModelVersion != tc.version {
			t.Fatalf("model version = %q, want %q", got[1].ModelVersion, tc.version)
		}
	}
}
