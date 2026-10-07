package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"eigenflux_server/pkg/es"
	"eigenflux_server/rpc/sort/discovery"
	searchindex "eigenflux_server/rpc/sort/discovery/index"
	"github.com/stretchr/testify/require"
)

// Keep historical documents unchanged: compatibility must work without backfill.
func TestHistoricalLanguageFiltersElasticsearch(t *testing.T) {
	endpoint := os.Getenv("DISCOVERY_TEST_ES")
	if endpoint == "" {
		t.Skip("isolated loopback DISCOVERY_TEST_ES required")
	}
	u, err := url.Parse(endpoint)
	require.NoError(t, err)
	ip := net.ParseIP(u.Hostname())
	require.True(t, u.Hostname() == "localhost" || ip != nil && ip.IsLoopback(), "language fixtures must not write to a remote cluster")
	prior := es.Client
	t.Cleanup(func() { es.Client = prior })
	t.Setenv("ES_URL", endpoint)
	require.NoError(t, es.InitClient())
	ctx := context.Background()
	prefix := fmt.Sprintf("discovery-language-%d", time.Now().UnixNano())
	indices := []string{}
	t.Cleanup(func() {
		if len(indices) > 0 {
			resp, err := es.Client.Indices.Delete(indices)
			if err == nil {
				resp.Body.Close()
			}
		}
	})
	languages := []string{"en", "English", "english", "eNgLiSh", "zh", "中文", "chinese", "Chinese", "en-US", "EN-us", "zh-CN", "zh-TW", "English · Chinese", "Spanish", "Englİsh", " English ", "EN"}
	for _, kind := range []discovery.Kind{discovery.Agent, discovery.Broadcast, discovery.Commission} {
		index := prefix + "-" + string(kind)
		properties := map[string]any{
			"active": map[string]any{"type": "boolean"}, "retrieval_slots": searchindex.SlotsMapping(),
			"embedding": map[string]any{"type": "dense_vector", "dims": 2, "index": true, "similarity": "cosine"},
		}
		for _, field := range []string{"id", "author_agent_id", "agent_id", "version", "projection_version", "commission_id", "catalogue_version", "seller_agent_id"} {
			properties[field] = map[string]any{"type": "long"}
		}
		for _, field := range []string{"content", "summary", "search_text", "display_name", "title"} {
			properties[field] = map[string]any{"type": "text"}
		}
		properties["lang"] = map[string]any{"type": "text", "fields": map[string]any{"keyword": map[string]any{"type": "keyword"}}}
		raw, err := json.Marshal(map[string]any{"mappings": map[string]any{"dynamic": "strict", "properties": properties}})
		require.NoError(t, err)
		resp, err := es.Client.Indices.Create(index, es.Client.Indices.Create.WithBody(bytes.NewReader(raw)))
		require.NoError(t, err)
		require.False(t, resp.IsError(), resp.String())
		resp.Body.Close()
		indices = append(indices, index)
		for pos, language := range languages {
			id := pos + 1
			doc := map[string]any{"embedding": []float32{1, 0}, "retrieval_slots": searchindex.Slots{Lang: []string{language}}}
			switch kind {
			case discovery.Agent:
				doc["agent_id"], doc["version"], doc["projection_version"], doc["active"], doc["search_text"], doc["display_name"] = id, 1, 1, true, "language fixture", "language fixture"
			case discovery.Commission:
				doc["commission_id"], doc["catalogue_version"], doc["seller_agent_id"], doc["active"], doc["search_text"], doc["title"] = id, 1, 999, true, "language fixture", "language fixture"
			case discovery.Broadcast:
				doc["id"], doc["author_agent_id"], doc["content"], doc["summary"], doc["lang"] = id, 999, "language fixture", "language fixture", language
			}
			raw, err = json.Marshal(doc)
			require.NoError(t, err)
			resp, err = es.Client.Index(index, bytes.NewReader(raw), es.Client.Index.WithDocumentID(strconv.Itoa(id)), es.Client.Index.WithRefresh("true"))
			require.NoError(t, err)
			require.False(t, resp.IsError(), resp.String())
			resp.Body.Close()
		}
		source := &discovery.Source{AgentIndex: index, BroadcastIndex: index, CommissionIndex: index}
		for _, channel := range []string{"lexical", "dense"} {
			for _, tc := range []struct {
				language string
				want     []int64
			}{{"en", []int64{1, 2, 3, 4, 17}}, {"English", []int64{1, 2, 3, 4, 17}},
				{"zh", []int64{5, 6, 7, 8}}, {"中文", []int64{5, 6, 7, 8}}, {"chinese", []int64{5, 6, 7, 8}},
				{"en-US", []int64{9, 10}}, {"zh-CN", []int64{11}}, {"zh-TW", []int64{12}},
				{"English · Chinese", []int64{13}}, {"Spanish", []int64{14}}, {"Englİsh", []int64{15}}, {" English ", []int64{16}}} {
				t.Run(string(kind)+"/"+channel+"/"+tc.language, func(t *testing.T) {
					c := discovery.Context{ID: 1, OwnerID: 9999, State: "active", Query: "language fixture", Kinds: []discovery.Kind{kind}, Vector: []float32{1, 0}, Filters: discovery.Filters{Lang: []string{tc.language}, ExcludeAuthors: []string{"9999"}}}
					docs, err := source.Recall(ctx, c, kind, channel, 20)
					require.NoError(t, err)
					ids := []int64{}
					for _, doc := range docs {
						ids = append(ids, doc.Ref.ID)
					}
					sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
					require.Equal(t, tc.want, ids, "ES must retrieve equivalent historical values and exclude other locales/composite text")
					// Compare every indexed row with the authoritative evaluator,
					// including rows ES must exclude rather than only returned rows.
					for pos, language := range languages {
						doc := discovery.Document{Ref: discovery.SourceRef{Type: kind, ID: int64(pos + 1)}, AuthorID: 999, Active: true, Visible: true, Slots: searchindex.Slots{Lang: []string{language}}}
						accepted := discovery.Check(c, doc, discovery.Search, 100) == ""
						found := false
						for _, id := range tc.want {
							found = found || id == doc.Ref.ID
						}
						require.Equal(t, found, accepted, "ES prefilter and authority must agree for %q", language)
					}
				})
			}
		}
	}
}
