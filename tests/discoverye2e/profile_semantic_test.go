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
	sortdal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/discovery"
	"github.com/stretchr/testify/require"
)

// Real HTTP -> Feed -> Sort -> ES/forward/Redis with the profile-semantic
// experiment on: a profile match without any keyword overlap is delivered,
// while a keyword match unrelated to the profile is gated out.
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
	got := map[int64]bool{}
	for _, item := range result.Items {
		got[item.Ref.ID] = true
	}
	require.True(t, got[related], "profile match without keyword overlap must be recalled: %+v", result)
	require.False(t, got[unrelated], "keyword match unrelated to the profile must be gated: %+v", result)
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
	require.True(t, strings.HasSuffix(sample.Search.Score.Version, ":profile_semantic_v1"), sample.Search.Score.Version)
	require.InDelta(t, 1, sample.Search.Score.Features["profile_cosine"], .00001)
	require.NotNil(t, sample.Search.ProfileRank)
	require.Nil(t, sample.Search.LR, "profile-ranked candidates bypass the discovery LR scorer")
}
