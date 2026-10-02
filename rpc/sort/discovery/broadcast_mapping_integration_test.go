package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"eigenflux_server/pkg/es"
	"eigenflux_server/rpc/sort/discovery"
	searchindex "eigenflux_server/rpc/sort/discovery/index"

	"github.com/stretchr/testify/require"
)

// Documents predate the mapping upgrade, just as in the production rollout.
// Queries execute against all three generations in real ES, in both channels.
func TestBroadcastHistoricalMappingsAndExactFilters(t *testing.T) {
	url := os.Getenv("DISCOVERY_TEST_ES")
	if url == "" {
		t.Skip("isolated DISCOVERY_TEST_ES required")
	}
	t.Setenv("ES_URL", url)
	require.NoError(t, es.InitClient())
	ctx := context.Background()
	prefix := fmt.Sprintf("discovery-mapping-%d", time.Now().UnixNano())
	indices := []string{prefix + "-old", prefix + "-text-slots", prefix + "-keyword-slots"}
	t.Cleanup(func() {
		resp, err := es.Client.Indices.Delete(indices)
		if err == nil {
			resp.Body.Close()
		}
	})
	language := es.BuildIndexMapping(2)["properties"].(map[string]any)["lang"]
	for i, index := range indices {
		properties := map[string]any{
			"lang": language, "content": map[string]any{"type": "text"},
			"embedding": map[string]any{"type": "dense_vector", "dims": 2, "index": true, "similarity": "cosine"},
		}
		if i == 1 {
			properties["retrieval_slots"] = map[string]any{"properties": map[string]any{"lang": language}}
		} else if i == 2 {
			properties["retrieval_slots"] = searchindex.SlotsMapping()
		}
		raw, err := json.Marshal(map[string]any{"mappings": map[string]any{"properties": properties}})
		require.NoError(t, err)
		resp, err := es.Client.Indices.Create(index, es.Client.Indices.Create.WithBody(bytes.NewReader(raw)))
		require.NoError(t, err)
		require.False(t, resp.IsError(), resp.String())
		resp.Body.Close()
		for j, lang := range []string{"en", "en-US", "zh", "zh-CN"} {
			id := int64(i*10 + j + 1)
			doc := map[string]any{"id": id, "content": "language fixture", "lang": lang, "embedding": []float32{1, 0}}
			if i > 0 {
				slots := map[string]any{"lang": []string{"fr"}}
				if i == 2 {
					slots["provider_region"] = []string{"US"}
				}
				doc["retrieval_slots"] = slots
			}
			raw, err = json.Marshal(doc)
			require.NoError(t, err)
			resp, err = es.Client.Index(index, bytes.NewReader(raw), es.Client.Index.WithDocumentID(fmt.Sprint(id)), es.Client.Index.WithRefresh("true"))
			require.NoError(t, err)
			require.False(t, resp.IsError(), resp.String())
			resp.Body.Close()
		}
	}
	require.ErrorContains(t, es.EnsureRetrievalSlots(ctx, indices[1]), "retrieval_slots.lang", "the shared slot upgrade reproduces the production conflict")
	for attempt := 0; attempt < 2; attempt++ {
		require.NoError(t, es.EnsureBroadcastRetrievalFields(ctx, prefix+"-*"), "upgrade and repeated startup must both succeed")
	}
	resp, err := es.Client.Indices.GetMapping(es.Client.Indices.GetMapping.WithIndex(indices...))
	require.NoError(t, err)
	var mappings map[string]struct {
		Mappings struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"mappings"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&mappings))
	resp.Body.Close()
	for i, index := range indices {
		var slots struct {
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
		}
		require.NoError(t, json.Unmarshal(mappings[index].Mappings.Properties["retrieval_slots"], &slots))
		require.Equal(t, "keyword", slots.Properties["provider_region"].Type)
		require.Equal(t, []string{"", "text", "keyword"}[i], slots.Properties["lang"].Type, "do not add or rewrite unused slot language")
	}
	source := &discovery.Source{BroadcastIndex: strings.Join(indices, ",")}
	for _, channel := range []string{"lexical", "dense"} {
		for j, lang := range []string{"en", "en-US", "zh", "zh-CN"} {
			t.Run(channel+"/"+lang, func(t *testing.T) {
				c := discovery.Context{Query: "language fixture", Vector: []float32{1, 0}, Filters: discovery.Filters{Lang: []string{lang}, ExcludeAuthors: []string{}}}
				docs, err := source.Recall(ctx, c, discovery.Broadcast, channel, 20)
				require.NoError(t, err)
				ids := []int64{}
				for _, d := range docs {
					ids = append(ids, d.Ref.ID)
				}
				require.ElementsMatch(t, []int64{int64(j + 1), int64(j + 11), int64(j + 21)}, ids, "exact language must neither split region codes nor use slot language")
				c.Filters.ProviderRegion = []string{"US"}
				docs, err = source.Recall(ctx, c, discovery.Broadcast, channel, 20)
				require.NoError(t, err)
				require.Len(t, docs, 1, "mapping addition must not invent missing historical region evidence")
				require.Equal(t, int64(j+21), docs[0].Ref.ID)
			})
		}
	}
	t.Run("RequiredRegionTypeConflictStillFails", func(t *testing.T) {
		index := prefix + "-region-conflict"
		indices = append(indices, index)
		body := `{"mappings":{"properties":{"retrieval_slots":{"properties":{"provider_region":{"type":"text"}}}}}}`
		resp, err := es.Client.Indices.Create(index, es.Client.Indices.Create.WithBody(strings.NewReader(body)))
		require.NoError(t, err)
		require.False(t, resp.IsError(), resp.String())
		resp.Body.Close()
		err = es.EnsureBroadcastRetrievalFields(ctx, index)
		require.ErrorContains(t, err, index)
		require.ErrorContains(t, err, "provider_region")
	})
}
