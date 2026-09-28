package discovery

import (
	"context"
	"testing"

	"eigenflux_server/pkg/es"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestBroadcastRecallPoolsNeedNoElasticsearch(t *testing.T) {
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { r.Close() })
	prior := es.Client
	es.Client = nil
	t.Cleanup(func() { es.Client = prior })
	ctx := context.Background()
	s := Source{Redis: r, RecallNamespace: "test"}
	for _, channel := range []string{"hot_recall", "new_recall", "new_ugc_recall"} {
		require.NoError(t, r.Set(ctx, "test:"+channel+":active_version", "v1", 0).Err())
		require.NoError(t, r.Set(ctx, "test:"+channel+":v1:index", "7,8", 0).Err())
		rows, err := s.Recall(ctx, Context{}, Broadcast, channel, 1)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.EqualValues(t, 7, rows[0].Ref.ID)
		require.Empty(t, rows[0].Version)
		require.Empty(t, rows[0].Text)
	}
}
func TestBroadcastFingerprintExcludesRankingAttributes(t *testing.T) {
	d := baseDoc(Broadcast)
	d.Quality = .3
	before := broadcastVersion(d)
	d.Quality = .9
	d.FreshAt = 100
	d.SourceUpdatedAt = 200
	d.GroupID = 9
	d.URL = "changed"
	d.ContentType = "info"
	require.Equal(t, before, broadcastVersion(d))
	d.Text = "changed searchable content"
	require.NotEqual(t, before, broadcastVersion(d))
	for _, channel := range []string{"lexical", "dense"} {
		q, err := Query(baseContext(), Broadcast, channel, 10)
		require.NoError(t, err)
		fields := q["_source"].([]string)
		for _, field := range []string{"embedding", "quality_score", "created_at", "updated_at", "group_id"} {
			require.NotContains(t, fields, field)
		}
	}
}

func TestTextEvidenceIsRequestedOnlyForExclusions(t *testing.T) {
	for _, kind := range []Kind{Agent, Commission} {
		c := baseContext()
		c.Filters.ExcludeTerms = nil
		plain, err := Query(c, kind, "lexical", 10)
		require.NoError(t, err)
		require.NotContains(t, plain["_source"], "search_text")
		c.Filters.ExcludeTerms = []string{"设计", "design"}
		filtered, err := Query(c, kind, "dense", 10)
		require.NoError(t, err)
		require.Contains(t, filtered["_source"], "search_text")
		for _, field := range []string{"embedding", "activity_at", "updated_at", "quality_score", "completion_rate_bps"} {
			require.NotContains(t, filtered["_source"], field)
		}
		if kind == Commission {
			require.Contains(t, plain["_source"], "title")
		}
	}
}
