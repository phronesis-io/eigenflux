package discoverye2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"eigenflux_server/pkg/impr"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/recall"
	"eigenflux_server/rpc/sort/discovery"
	"github.com/stretchr/testify/require"
)

func TestDiscoveryFriendAndSurfacedSwing(t *testing.T) {
	namespace := fmt.Sprintf("rec:discovery-e2e-%d", time.Now().UnixNano())
	s := startStack(t, map[string]string{"FRIEND_FEED_ENABLED": "true", "ENABLE_SWING_I2I_RECALL": "true", "REC_REDIS_NAMESPACE": namespace})
	ctx := context.Background()
	t.Cleanup(func() {
		keys, err := mq.RDB.Keys(ctx, namespace+":*").Result()
		require.NoError(t, err)
		if len(keys) > 0 {
			require.NoError(t, mq.RDB.Del(ctx, keys...).Err())
		}
	})
	seed := s.item + 1000
	// Impression alone cannot produce a Swing recommendation.
	require.NoError(t, mq.RDB.SAdd(ctx, fmt.Sprintf(impr.KeyItemIDs, s.other), seed).Err())
	require.NoError(t, mq.RDB.Set(ctx, namespace+":swing_i2i:active_version", "v1", time.Hour).Err())
	require.NoError(t, mq.RDB.Set(ctx, fmt.Sprintf("%s:swing_i2i:v1:item:%d:scored_neighbors", namespace, seed), fmt.Sprintf("%d:1", s.item), time.Hour).Err())
	request := discovery.Request{SourceKinds: []discovery.Kind{discovery.Broadcast}, Limit: 2}
	recommendOther := func() discovery.Response {
		return decode[discovery.Response](t, s.call(t, "POST", "/api/v2/discovery/recommendations", s.otherToken, "", request, 200))
	}
	require.Empty(t, recommendOther().Items)
	require.NoError(t, recall.NewSurfaceHistoryStore(mq.RDB, namespace).Record(ctx, recall.SurfaceEvent{AgentID: s.other, ItemID: seed, ReportedAt: time.Now().UnixMilli()}))
	swingBefore := recallCounter(t, "FEED_RPC_PORT", "recall_impression_total", "swing_i2i")
	swing := recommendOther()
	require.Len(t, swing.Items, 1)
	require.Equal(t, s.item, swing.Items[0].Ref.ID)

	assertChannel := func(impression, channel string) {
		require.Eventually(t, func() bool {
			var count int64
			err := s.db.Table("replay_logs").Where("impression_id=?", impression).Count(&count).Error
			return err == nil && count == 1
		}, 10*time.Second, 25*time.Millisecond)
		var raw string
		require.NoError(t, s.db.Table("replay_logs").Select("item_features").Where("impression_id=?", impression).Scan(&raw).Error)
		var feature struct {
			Search discovery.Candidate `json:"search"`
		}
		require.NoError(t, json.Unmarshal([]byte(raw), &feature))
		require.Contains(t, feature.Search.Document.Channels, channel)
		if channel == "friend" {
			require.Equal(t, "friend", feature.Search.Context.Origin)
			require.Zero(t, feature.Search.Context.NeedID())
			require.Nil(t, feature.Search.Context.CapturedNeed)
		}
	}
	assertChannel(swing.ImpressionID, "swing_i2i")
	require.Equal(t, swingBefore+1, recallCounter(t, "FEED_RPC_PORT", "recall_impression_total", "swing_i2i"))
	require.Eventually(t, func() bool { return mq.RDB.SIsMember(ctx, fmt.Sprintf(impr.KeyItemIDs, s.other), s.item).Val() }, 3*time.Second, 25*time.Millisecond)
	require.Empty(t, recommendOther().Items, "already delivered neighbors must be excluded")
	// Friend-only content survives lack of lexical/dense relevance for this Need.
	s.sql(t, "INSERT INTO user_relations(from_uid,to_uid,rel_type,created_at) VALUES(?,?,1,?)", s.owner, s.author, time.Now().UnixMilli())
	input := s.need(t, "broadcast")
	input.Target.Goal = "unrelated-quasar-physics"
	input.Constraints.Lang = []string{"zh"}
	captured := s.capture(t, input)
	request.NeedIDs = []string{fmt.Sprint(captured.NeedInputID)}
	request.Filters.Lang = []string{"zh"}
	require.Empty(t, s.recommend(t, request, "").Items, "friend bypass must not defeat hard filters")
	request.Filters = discovery.Filters{}
	// A single slot has floor(1/2)=0 friend capacity. Legacy Feed must not leak
	// prepared item details for candidates removed by the source ceiling.
	feed := decode[map[string]any](t, s.call(t, "POST", "/api/v2/feed", s.token, "", map[string]any{"limit": 1}, 200))
	require.Empty(t, feed["items"])
	friendBefore := recallCounter(t, "FEED_RPC_PORT", "recall_impression_total", "friend")
	friend := s.recommend(t, request, "")
	require.Len(t, friend.Items, 1)
	require.Equal(t, s.item, friend.Items[0].Ref.ID)

	assertChannel(friend.ImpressionID, "friend")
	require.Equal(t, friendBefore+1, recallCounter(t, "FEED_RPC_PORT", "recall_impression_total", "friend"))
}
