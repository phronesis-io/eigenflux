package discoverye2e

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	embcodec "eigenflux_server/pkg/embedding"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/mq"
	sortdal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/discovery"
	"eigenflux_server/rpc/sort/discovery/queryprocessing"
	"github.com/stretchr/testify/require"
)

// Real HTTP -> Feed -> Sort -> ES/forward/Redis with the profile-semantic
// experiment on: a profile match without any keyword overlap is delivered
// ahead of a keyword match unrelated to the profile, which stays eligible.
func TestDiscoveryProfileSemanticRecommendation(t *testing.T) {
	s := startStack(t, map[string]string{"DISCOVERY_PROFILE_SEMANTIC_BPS": "10000"})
	ctx := context.Background()
	now := time.Now()
	query := fmt.Sprintf("semantictoken%d", s.owner)
	s.changeInputs(t, "UPDATE agent_context_revisions SET compiled_context=?::jsonb WHERE agent_id=?", fmt.Sprintf(`{"intent_actions":[{"watch_for":%q,"trigger_when":""}]}`, query), s.owner)
	s.sql(t, "INSERT INTO agent_profiles(agent_id,status,profile_embedding,updated_at) VALUES(?,3,?,?) ON CONFLICT(agent_id) DO UPDATE SET profile_embedding=EXCLUDED.profile_embedding", s.owner, embcodec.Encode(s.vector), now.UnixMilli())
	orthogonal := make([]float32, len(s.vector))
	orthogonal[1] = 1
	related, unrelated := s.item+40001, s.item+40002
	for _, f := range []struct {
		id     int64
		text   string
		vector []float32
	}{{related, "battery cathode research update", s.vector}, {unrelated, query, orthogonal}} {
		s.sql(t, "INSERT INTO raw_items(item_id,author_agent_id,raw_content,raw_url,created_at) VALUES(?,?,?,'',?)", f.id, s.author, f.text, now.UnixMilli())
		s.sql(t, "INSERT INTO processed_items(item_id,status,summary,lang,quality_score,group_id,updated_at,broadcast_type,source_type) VALUES(?,3,?,'en',0.8,?,?,'info','original')", f.id, f.text, f.id, now.UnixMilli())
		require.NoError(t, sortdal.IndexItem(ctx, &sortdal.Item{ID: f.id, AuthorAgentID: s.author, Content: f.text, Summary: f.text, Lang: "en", Type: "info", SourceType: "original", QualityScore: .8, GroupID: f.id, CreatedAt: now, UpdatedAt: now, Embedding: f.vector}))
	}
	t.Cleanup(func() {
		ids := []int64{related, unrelated}
		s.sql(t, "DELETE FROM processed_items WHERE item_id IN ?", ids)
		s.sql(t, "DELETE FROM raw_items WHERE item_id IN ?", ids)
		for _, id := range ids {
			r, err := es.Client.DeleteByQuery([]string{es.ReadIndexPattern}, bytes.NewBufferString(fmt.Sprintf(`{"query":{"term":{"id":%d}}}`, id)), es.Client.DeleteByQuery.WithRefresh(true))
			if err == nil {
				r.Body.Close()
			}
			require.NoError(t, err)
		}
	})
	response, err := es.Client.Indices.Refresh(es.Client.Indices.Refresh.WithIndex(es.IndexName))
	require.NoError(t, err)
	response.Body.Close()

	result := s.recommend(t, discovery.Request{SourceKinds: []discovery.Kind{discovery.Broadcast}, Limit: 20}, "profile-semantic")
	at := map[int64]int{}
	for i, item := range result.Items {
		at[item.Ref.ID] = i + 1
	}
	require.NotZero(t, at[related], "profile match without keyword overlap must be recalled: %+v", result)
	require.NotZero(t, at[unrelated], "a rule-eligible keyword match stays eligible: %+v", result)
	require.Less(t, at[related], at[unrelated], "profile similarity orders the page: %+v", result)
	for _, reason := range result.Reasons {
		require.False(t, strings.HasPrefix(reason, "profile_semantic:"), "unexpected fallback: %v", result.Reasons)
	}
	s.waitSamples(t, result.ImpressionID, len(result.Items))
	var raw string
	require.NoError(t, s.db.Table("replay_logs").Select("item_features").Where("impression_id=? AND item_id=?", result.ImpressionID, related).Scan(&raw).Error)
	sample := decode[struct {
		Search discovery.Candidate `json:"search"`
	}](t, []byte(raw))
	require.Equal(t, "rule_score", sample.Search.Score.Kind, "the LR training contract is retained")
	require.True(t, strings.HasSuffix(sample.Search.Score.Version, ":profile_semantic_v2"), sample.Search.Score.Version)
	require.InDelta(t, 1, sample.Search.Score.Features["profile_cosine"], .00001)
	require.NotNil(t, sample.Search.SemanticRank)
	require.Nil(t, sample.Search.LR, "profile-ranked candidates bypass the discovery LR scorer")
}

// With a captured broadcast Need, the Need's own vector admits a match with no
// keyword overlap and orders it ahead of a keyword match unrelated to the Need.
func TestDiscoveryNeedSemanticRecommendation(t *testing.T) {
	s := startStack(t, map[string]string{"DISCOVERY_PROFILE_SEMANTIC_BPS": "10000"})
	ctx := context.Background()
	now := time.Now()
	defer s.changeInputs(t, "DELETE FROM need_inputs WHERE agent_id=?", s.owner)
	in := s.need(t, "broadcast")
	in.Target.Goal = fmt.Sprintf("solid electrolyte suppliers needsem%d", s.owner)
	s.capture(t, in)
	query := queryprocessing.Process(queryprocessing.NeedText(in.Target.Goal, in.Target.Context), queryprocessing.Options{}).Normalized
	t.Cleanup(func() { mq.RDB.Del(ctx, s.needVectors.Key(query)) })
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); s.needWorker.Start(workerCtx) }()
	require.Eventually(t, func() bool { _, err := s.needVectors.Read(ctx, query); return err == nil }, 15*time.Second, 50*time.Millisecond)
	stop()
	<-done
	orthogonal := make([]float32, len(s.vector))
	orthogonal[1] = 1
	related, unrelated := s.item+50001, s.item+50002
	for _, f := range []struct {
		id     int64
		text   string
		vector []float32
	}{{related, "anode materials quarterly digest", s.vector}, {unrelated, in.Target.Goal, orthogonal}} {
		s.sql(t, "INSERT INTO raw_items(item_id,author_agent_id,raw_content,raw_url,created_at) VALUES(?,?,?,'',?)", f.id, s.author, f.text, now.UnixMilli())
		s.sql(t, "INSERT INTO processed_items(item_id,status,summary,lang,quality_score,group_id,updated_at,broadcast_type,source_type) VALUES(?,3,?,'en',0.8,?,?,'info','original')", f.id, f.text, f.id, now.UnixMilli())
		require.NoError(t, sortdal.IndexItem(ctx, &sortdal.Item{ID: f.id, AuthorAgentID: s.author, Content: f.text, Summary: f.text, Lang: "en", Type: "info", SourceType: "original", QualityScore: .8, GroupID: f.id, CreatedAt: now, UpdatedAt: now, Embedding: f.vector}))
	}
	t.Cleanup(func() {
		ids := []int64{related, unrelated}
		s.sql(t, "DELETE FROM processed_items WHERE item_id IN ?", ids)
		s.sql(t, "DELETE FROM raw_items WHERE item_id IN ?", ids)
		for _, id := range ids {
			r, err := es.Client.DeleteByQuery([]string{es.ReadIndexPattern}, bytes.NewBufferString(fmt.Sprintf(`{"query":{"term":{"id":%d}}}`, id)), es.Client.DeleteByQuery.WithRefresh(true))
			if err == nil {
				r.Body.Close()
			}
			require.NoError(t, err)
		}
	})
	response, err := es.Client.Indices.Refresh(es.Client.Indices.Refresh.WithIndex(es.IndexName))
	require.NoError(t, err)
	response.Body.Close()

	result := s.recommend(t, discovery.Request{SourceKinds: []discovery.Kind{discovery.Broadcast}, Limit: 20}, "need-semantic")
	require.Equal(t, "need_input", result.Origin, "%+v", result)
	at := map[int64]int{}
	for i, item := range result.Items {
		at[item.Ref.ID] = i + 1
	}
	require.NotZero(t, at[related], "Need match without keyword overlap must be admitted: %+v", result)
	require.NotZero(t, at[unrelated], "a rule-eligible keyword match stays eligible: %+v", result)
	require.Less(t, at[related], at[unrelated], "Need similarity orders the page: %+v", result)
	for _, reason := range result.Reasons {
		require.False(t, strings.HasPrefix(reason, "need_semantic:"), "unexpected fallback: %v", result.Reasons)
	}
	s.waitSamples(t, result.ImpressionID, len(result.Items))
	var raw string
	require.NoError(t, s.db.Table("replay_logs").Select("item_features").Where("impression_id=? AND item_id=?", result.ImpressionID, related).Scan(&raw).Error)
	sample := decode[struct {
		Search discovery.Candidate `json:"search"`
	}](t, []byte(raw))
	require.True(t, strings.HasSuffix(sample.Search.Score.Version, ":need_semantic_v1"), sample.Search.Score.Version)
	require.InDelta(t, 1, sample.Search.Score.Features["need_cosine"], .00001)
	require.NotNil(t, sample.Search.SemanticRank)
	require.Nil(t, sample.Search.LR)
}
