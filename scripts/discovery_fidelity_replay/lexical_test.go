package main

import (
	"context"
	"eigenflux_server/pkg/es"
	"eigenflux_server/rpc/sort/discovery"
	elasticsearch "github.com/elastic/go-elasticsearch/v8"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLexicalDeltaAnchorsProductionScoresAndCanAdmitFullPoolCandidate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "before") {
			_, _ = w.Write([]byte(`{"hits":{"hits":[{"_source":{"id":1},"_score":2},{"_source":{"id":2},"_score":1},{"_source":{"id":3},"_score":1}]}}`))
		} else {
			_, _ = w.Write([]byte(`{"hits":{"hits":[{"_source":{"id":1},"_score":1},{"_source":{"id":2},"_score":1},{"_source":{"id":3},"_score":4}]}}`))
		}
	}))
	defer srv.Close()
	priorClient := es.Client
	defer func() { es.Client = priorClient }()
	var err error
	es.Client, err = elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{srv.URL}})
	require.NoError(t, err)
	c := discovery.Context{ID: 5, Query: "topic"}
	a := discovery.Document{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 1}, Version: "old", Lexical: 20}
	b := a
	b.Ref.ID = 2
	b.Lexical = 10
	fresh := a
	fresh.Version = "new"
	extra := fresh
	extra.Ref.ID = 3
	s := &snapshot{Channels: map[string]channel{channelKey(c, discovery.Broadcast, "lexical"): {ContextID: 5, Kind: discovery.Broadcast, Name: "lexical", Limit: 2, Documents: storeDocs([]discovery.Document{a, b})}}}
	p := poolSource{snap: s, reference: "before", lexical: &discovery.Source{BroadcastIndex: "after"}, changed: map[int64]bool{1: true, 3: true}, docs: map[int64]discovery.Document{1: fresh, 2: b, 3: extra}}
	got, err := p.calibratedLexical(context.Background(), c, discovery.Broadcast, 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, int64(3), got[0].Ref.ID)
	require.Equal(t, float64(40), got[0].Lexical)
	require.Equal(t, int64(1), got[1].Ref.ID)
	require.Equal(t, float64(10), got[1].Lexical)
	// A local match absent from an unfilled production channel is not evidence
	// that the release repairs its index visibility.
	key := channelKey(c, discovery.Broadcast, "lexical")
	unfilled := s.Channels[key]
	unfilled.Limit = 3
	s.Channels[key] = unfilled
	got, err = p.calibratedLexical(context.Background(), c, discovery.Broadcast, 3)
	require.NoError(t, err)
	require.Len(t, got, 2)
	for _, d := range got {
		require.NotEqual(t, int64(3), d.Ref.ID)
	}
	unfilled.Limit = 2
	s.Channels[key] = unfilled
	p.changed = nil
	got, err = p.calibratedLexical(context.Background(), c, discovery.Broadcast, 2)
	require.NoError(t, err)
	require.Equal(t, []discovery.Document{a, b}, got)
}
