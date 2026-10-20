package legacy

import (
	"context"
	"testing"
	"time"

	"eigenflux_server/pkg/cache"
	"eigenflux_server/pkg/config"
	sortDal "eigenflux_server/rpc/sort/dal"
)

func TestDiscoveryPGCAuthorsNeverGuesses(t *testing.T) {
	s := &Service{cfg: &config.Config{}, contentClassCache: cache.NewLocal[int64, bool](8)}
	s.contentClassCache.PutUntil(1, true, time.Now().Add(time.Hour))
	s.contentClassCache.PutUntil(2, false, time.Now().Add(time.Hour))
	got, err := s.DiscoveryPGCAuthors(context.Background(), []int64{1, 2, 0})
	if err != nil || !got[1] || got[2] || len(got) != 3 {
		t.Fatalf("cached verdicts: %v %v", got, err)
	}
	if _, err := s.DiscoveryPGCAuthors(context.Background(), []int64{1, 3}); err == nil {
		t.Fatal("an unresolved author must be an error, not UGC")
	}
	classes := s.resolveContentClasses(context.Background(), []sortDal.Item{{ID: 10, AuthorAgentID: 1}, {ID: 11, AuthorAgentID: 3}})
	if classes[10] != contentClassPGC || classes[11] != contentClassUGC {
		t.Fatalf("rerank classification keeps treating unresolved authors as UGC: %v", classes)
	}
}
