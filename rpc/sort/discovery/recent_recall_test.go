package discovery

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"eigenflux_server/pkg/recallsource"
	searchindex "eigenflux_server/rpc/sort/discovery/index"
	"github.com/stretchr/testify/require"
)

type recentSource struct {
	*sourceFake
	mu    sync.Mutex
	calls []struct {
		kind   Kind
		origin string
		limit  int
		at     int64
	}
	docs []Document
}

func (s *recentSource) Recall(_ context.Context, c Context, k Kind, ch string, limit int) ([]Document, error) {
	if ch != "lexical_recent" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, struct {
		kind   Kind
		origin string
		limit  int
		at     int64
	}{k, c.Origin, limit, c.retrievalAt})
	return s.docs, nil
}

func TestRecentRecallRemainsBoundedAndSubjectToGates(t *testing.T) {
	e, fake, _ := engineFixture()
	fake.owner.Clauses = []string{"design"}
	e.FriendFeedEnabled = true
	source := &recentSource{sourceFake: fake, docs: []Document{
		{Ref: SourceRef{Broadcast, 20}, AuthorID: 2, Version: "1", Active: true, Visible: true, Lexical: 10, FreshAt: 100, Slots: searchindex.Slots{Lang: []string{"zh"}}},
		{Ref: SourceRef{Broadcast, 21}, AuthorID: 2, Version: "1", Active: true, Visible: true, Lexical: 0, FreshAt: 100, Slots: searchindex.Slots{Lang: []string{"zh"}}},
		{Ref: SourceRef{Broadcast, 22}, AuthorID: 2, Version: "1", Active: true, Visible: true, Lexical: 10, ExpiresAt: 99, Slots: searchindex.Slots{Lang: []string{"zh"}}},
		{Ref: SourceRef{Broadcast, 24}, AuthorID: 2, Version: "1", Active: true, Visible: true, Lexical: 10, Slots: searchindex.Slots{Lang: []string{"en"}}},
		{Ref: SourceRef{Broadcast, 23}, AuthorID: 2, Version: "1", Active: true, Visible: true, Lexical: 10, Slots: searchindex.Slots{Lang: []string{"zh"}}},
	}}
	e.Sources = source
	fake.seen["broadcast:20"] = true
	x, err := e.Execute(context.Background(), 1, Request{Filters: Filters{Lang: []string{"zh"}}}, Recommendation, 100)
	require.NoError(t, err)
	require.Len(t, source.calls, 1)
	require.Equal(t, Broadcast, source.calls[0].kind)
	require.Equal(t, 20, source.calls[0].limit)
	require.Equal(t, int64(100), source.calls[0].at)
	// Zero relevance and expiry cannot be rescued by freshness. Seen items remain excluded.
	require.Len(t, x.Candidates, 1)
	require.Equal(t, int64(23), x.Candidates[0].Document.Ref.ID)
	require.Equal(t, recallsource.Keyword, x.Candidates[0].Document.RecallSources())
	require.Equal(t, []string{"keyword"}, matchTypes(x.Candidates[0].Document.Channels))
	source.calls = nil
	_, err = e.Execute(context.Background(), 1, Request{Query: "design", SourceKinds: []Kind{Broadcast}}, Search, 100)
	require.NoError(t, err)
	require.Empty(t, source.calls)
	fake.owner.Clauses = nil
	_, err = e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast}}, Recommendation, 100)
	require.NoError(t, err)
	require.Empty(t, source.calls, "empty context baseline must not add keyword recall")
	c := Context{retrievalAt: 100}
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "retrievalAt")
}

func TestRecentRecallTimeoutIsVisibleAndPreservesOrdinaryCandidates(t *testing.T) {
	e, source, _ := engineFixture()
	source.owner.Clauses = []string{"design"}
	source.docs = []Document{{Ref: SourceRef{Broadcast, 20}, AuthorID: 2, Version: "1", Active: true, Visible: true, Lexical: 10}}
	source.channelErrors = map[string]error{"lexical_recent": context.DeadlineExceeded}
	x, err := e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast}}, Recommendation, 100)
	require.NoError(t, err)
	require.Len(t, x.Candidates, 1)
	require.Contains(t, x.PartialReasons, "broadcast:lexical_recent_unavailable")
	require.Contains(t, x.Candidates[0].Document.Channels, "lexical")
	require.NotContains(t, x.Candidates[0].Document.Channels, "lexical_recent")
}
