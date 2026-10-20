package discoverye2e

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"eigenflux_server/pkg/bloomfilter"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/impr"
	"eigenflux_server/pkg/mq"
	sortdal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/discovery"
	"github.com/stretchr/testify/require"
)

// Real HTTP -> Feed -> Sort -> ES/forward/Redis: a consumed lexical head must
// not starve later matching documents. Both delivery ledgers advance normally.
func TestDiscoveryConsumedHeadRefill(t *testing.T) {
	s := startStack(t, map[string]string{"DISCOVERY_CONSUMED_REFILL_BPS": "10000"})
	ctx := context.Background()
	now := time.Now()
	ids := []int64{}
	history := []impr.ImprItem{}
	groups := []int64{}
	query := fmt.Sprintf("refilltoken%d", s.owner)
	s.changeInputs(t, "UPDATE agent_context_revisions SET compiled_context=?::jsonb WHERE agent_id=?", fmt.Sprintf(`{"intent_actions":[{"watch_for":%q,"trigger_when":""}]}`, query), s.owner)
	// Unrelated corpus keeps BM25 IDF meaningful for the matching fixture.
	for i := 0; i < 100; i++ {
		id := s.item + 30000 + int64(i)
		require.NoError(t, sortdal.IndexItem(ctx, &sortdal.Item{ID: id, AuthorAgentID: s.author, Content: "unrelated fixture", Summary: "unrelated", Lang: "en", CreatedAt: now, UpdatedAt: now, Embedding: s.vector}))
		ids = append(ids, id)
	}
	for i := 0; i < 92; i++ {
		id := s.item + 20000 + int64(i)
		ids = append(ids, id)
		text := query
		summary := query
		if i >= 90 {
			text = query + " " + strings.Repeat("unrelated ", 100)
			summary = "unrelated"
		}
		s.sql(t, "INSERT INTO raw_items(item_id,author_agent_id,raw_content,raw_url,created_at) VALUES(?,?,?,'',?)", id, s.author, text, now.UnixMilli())
		s.sql(t, "INSERT INTO processed_items(item_id,status,summary,lang,quality_score,group_id,updated_at,broadcast_type,source_type) VALUES(?,3,?,'en',0.8,?,?,'info','original')", id, summary, id, now.UnixMilli())
		require.NoError(t, sortdal.IndexItem(ctx, &sortdal.Item{ID: id, AuthorAgentID: s.author, Content: text, Summary: summary, Lang: "en", Type: "info", SourceType: "original", QualityScore: .8, GroupID: id, CreatedAt: now, UpdatedAt: now, Embedding: s.vector}))
		if i < 90 {
			groups = append(groups, id)
			history = append(history, impr.ImprItem{ItemID: id, GroupID: id})
		}
	}
	t.Cleanup(func() {
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
	require.NoError(t, impr.RecordImpressions(ctx, mq.RDB, s.owner, history))
	require.NoError(t, bloomfilter.NewBloomFilter(mq.RDB).Add(ctx, s.owner, groups))
	request := discovery.Request{SourceKinds: []discovery.Kind{discovery.Broadcast}, Limit: 1}
	first := s.recommend(t, request, "consumed-refill-first")
	require.Len(t, first.Items, 1, "%+v", first)
	require.Contains(t, ids[190:], first.Items[0].Ref.ID)
	require.Equal(t, first, s.recommend(t, request, "consumed-refill-first"), "retry must retain the same delivery")
	require.Eventually(t, func() bool {
		return mq.RDB.SIsMember(ctx, fmt.Sprintf(impr.KeyItemIDs, s.owner), first.Items[0].Ref.ID).Val()
	}, 5*time.Second, 25*time.Millisecond)
	second := s.recommend(t, request, "consumed-refill-second")
	require.Len(t, second.Items, 1)
	require.Contains(t, ids[190:], second.Items[0].Ref.ID)
	require.NotEqual(t, first.Items[0].Ref.ID, second.Items[0].Ref.ID)
	require.Eventually(t, func() bool {
		return mq.RDB.SIsMember(ctx, fmt.Sprintf(impr.KeyItemIDs, s.owner), second.Items[0].Ref.ID).Val()
	}, 5*time.Second, 25*time.Millisecond)
	require.Empty(t, s.recommend(t, request, "consumed-refill-third").Items)
}
