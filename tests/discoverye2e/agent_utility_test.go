package discoverye2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"eigenflux_server/pkg/agentutility"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/featureindex"
	"eigenflux_server/pkg/mq"
	sortdal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/discovery"

	"github.com/stretchr/testify/require"
)

func TestDiscoveryAgentUtility(t *testing.T) {
	namespace := fmt.Sprintf("rec:utility-e2e-%d", time.Now().UnixNano())
	s := startStack(t, map[string]string{"ENABLE_HOT_RECALL": "true", "REC_REDIS_NAMESPACE": namespace})
	ctx := context.Background()
	usefulID := s.item + 101
	content := "landing page design\nInstall the browser workflow:\nuvx mcp-server-browser --headless"
	now := time.Now().UnixMilli()
	s.sql(t, "INSERT INTO raw_items(item_id,author_agent_id,raw_content,created_at) VALUES(?,?,?,?)", usefulID, s.author, content, now)
	s.sql(t, "INSERT INTO processed_items(item_id,status,summary,broadcast_type,source_type,quality_score,lang,domains,keywords,group_id,updated_at) VALUES(?,3,'landing page design','info','original',0.8,'en',?,'landing-page',?,?)", usefulID, s.label, usefulID, now)
	t.Cleanup(func() {
		s.sql(t, "DELETE FROM processed_items WHERE item_id=?", usefulID)
		s.sql(t, "DELETE FROM raw_items WHERE item_id=?", usefulID)
		resp, err := es.Client.DeleteByQuery([]string{es.ReadIndexPattern}, bytes.NewBufferString(fmt.Sprintf(`{"query":{"term":{"id":%d}}}`, usefulID)), es.Client.DeleteByQuery.WithRefresh(true))
		require.NoError(t, err)
		resp.Body.Close()
		require.False(t, resp.IsError())
		keys, err := mq.RDB.Keys(ctx, namespace+":*").Result()
		require.NoError(t, err)
		if len(keys) > 0 {
			require.NoError(t, mq.RDB.Del(ctx, keys...).Err())
		}
		require.NoError(t, mq.RDB.Del(ctx, (featureindex.BroadcastIndex{Redis: mq.RDB}).Forward().Key(usefulID, "item")).Err())
	})
	require.NoError(t, sortdal.IndexItem(ctx, &sortdal.Item{ID: usefulID, AuthorAgentID: s.author, Content: content, Summary: "landing page design", Type: "info", SourceType: "original", Lang: "en", Domains: []string{s.label}, Keywords: []string{"landing-page"}, QualityScore: .8, GroupID: usefulID, CreatedAt: time.UnixMilli(now), UpdatedAt: time.UnixMilli(now), Embedding: s.vector}))
	refresh, err := es.Client.Indices.Refresh(es.Client.Indices.Refresh.WithIndex(es.IndexName))
	require.NoError(t, err)
	refresh.Body.Close()
	require.False(t, refresh.IsError())
	require.NoError(t, mq.RDB.Set(ctx, namespace+":hot_recall:active_version", "v1", time.Hour).Err())
	require.NoError(t, mq.RDB.Set(ctx, namespace+":hot_recall:v1:index", fmt.Sprintf("%d,%d", s.item, usefulID), time.Hour).Err())
	store := featureindex.BroadcastIndex{DB: s.db, Redis: mq.RDB}
	projected, err := store.Load(ctx, []int64{s.item, usefulID})
	require.NoError(t, err)
	require.Equal(t, agentutility.WorkflowRecipe, projected[usefulID].AgentUtility.Label())
	require.Equal(t, agentutility.Unknown, projected[s.item].AgentUtility.Label())
	// A hard language constraint still rejects both broadcasts before promotion.
	rejected := s.recommend(t, discovery.Request{SourceKinds: []discovery.Kind{discovery.Broadcast}, Limit: 1, Filters: discovery.Filters{Lang: []string{"zh"}}}, "utility-filter")
	require.Empty(t, rejected.Items)
	request := discovery.Request{SourceKinds: []discovery.Kind{discovery.Broadcast}, Limit: 1}
	before := recallCounter(t, "FEED_RPC_PORT", "discovery_broadcast_utility_delivered_total", "workflow_recipe")
	out := s.recommend(t, request, "utility-recommend")
	require.Len(t, out.Items, 1)
	require.Equal(t, usefulID, out.Items[0].Ref.ID)
	require.Equal(t, out, s.recommend(t, request, "utility-recommend"))
	s.waitSamples(t, out.ImpressionID, 1)
	var raw string
	require.NoError(t, s.db.Table("replay_logs").Select("item_features").Where("impression_id=? AND agent_id=?", out.ImpressionID, s.owner).Scan(&raw).Error)
	var saved struct {
		Search discovery.Candidate `json:"search"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &saved))
	require.Equal(t, agentutility.Version, saved.Search.Document.AgentUtility.Version)
	require.Contains(t, saved.Search.Reasons, "boost:agent_utility=workflow_recipe")
	require.Greater(t, saved.Search.FinalScore, saved.Search.Score.Value)
	// Unknown items remain available and are backfilled after the useful item is seen.
	require.Eventually(t, func() bool { return mq.RDB.SIsMember(ctx, fmt.Sprintf("impr:agent:%d:items", s.owner), usefulID).Val() }, 3*time.Second, 25*time.Millisecond)
	next := s.recommend(t, request, "utility-next")
	require.Len(t, next.Items, 1)
	require.Equal(t, s.item, next.Items[0].Ref.ID)
	// Explicit search retains its existing scorer/policies without utility boost.
	search := s.search(t, discovery.Request{Query: "landing page design", SourceKinds: []discovery.Kind{discovery.Broadcast}, Limit: 5}, "utility-search")
	require.Len(t, search.Items, 2)
	s.waitSamples(t, search.ImpressionID, 2)
	var features []string
	require.NoError(t, s.db.Table("replay_logs").Select("item_features::text").Where("impression_id=? AND agent_id=?", search.ImpressionID, s.owner).Scan(&features).Error)
	for _, feature := range features {
		require.NotContains(t, feature, "boost:agent_utility=")
	}
	require.Equal(t, before+1, recallCounter(t, "FEED_RPC_PORT", "discovery_broadcast_utility_delivered_total", "workflow_recipe"), "retries and search must not duplicate utility impressions")
}
