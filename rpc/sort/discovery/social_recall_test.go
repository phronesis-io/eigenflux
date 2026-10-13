package discovery

import (
	"context"
	"fmt"
	"testing"
	"time"

	"eigenflux_server/pkg/impr"
	"eigenflux_server/pkg/need"
	"eigenflux_server/pkg/recall"
	"eigenflux_server/pkg/recallsource"
	searchindex "eigenflux_server/rpc/sort/discovery/index"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestSurfacedSwingRecall(t *testing.T) {
	mr := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { r.Close() })
	ctx := context.Background()
	history := recall.NewSurfaceHistoryStore(r, "test")
	source := &Source{Redis: r, SwingRecall: recallsource.NewSwingI2IRecallSource(recall.NewRedisRecallReader(r, "test"), history, r, 2, 2)}
	c := Context{OwnerID: 1, Filters: Filters{ExcludeTerms: []string{"secret"}}}
	mr.Set("test:swing_i2i:active_version", "v1")
	mr.Set("test:swing_i2i:v1:item:10:scored_neighbors", "10:1,20:0.9,30:0.5,40:0.3")
	mr.Set("test:swing_i2i:v1:item:11:scored_neighbors", "30:0.8,40:0.3,50:0.1")
	mr.SAdd(fmt.Sprintf(impr.KeyItemIDs, 1), "10", "20")
	docs, err := source.Recall(ctx, c, Broadcast, "swing_i2i", 200)
	require.NoError(t, err)
	require.Empty(t, docs, "impressions must not become Swing seeds")
	now := time.Now().UnixMilli()
	require.NoError(t, history.Upsert(ctx, []recall.SurfaceEvent{{AgentID: 1, ItemID: 10, ReportedAt: now}, {AgentID: 1, ItemID: 11, ReportedAt: now}}))
	docs, err = source.Recall(ctx, c, Broadcast, "swing_i2i", 200)
	require.NoError(t, err)
	require.Len(t, docs, 2)
	require.Equal(t, int64(30), docs[0].Ref.ID, "summed neighbors must precede weaker candidates")
	require.Equal(t, int64(40), docs[1].Ref.ID)
	require.True(t, docs[0].NeedExclusionText)
	require.Empty(t, docs[0].Version, "pool must use current hydration")
	require.Zero(t, docs[0].Lexical, "Swing similarity is not BM25 or Need relevance")
	docs, err = source.Recall(ctx, Context{OwnerID: 2}, Broadcast, "swing_i2i", 200)
	require.NoError(t, err)
	require.Empty(t, docs, "another user's surfaced seeds must not leak")
	// A surfaced seed with no active neighbor generation is a channel error.
	source.SwingRecall = recallsource.NewSwingI2IRecallSource(recall.NewRedisRecallReader(r, "missing"), history, r, 2, 2)
	_, err = source.Recall(ctx, c, Broadcast, "swing_i2i", 200)
	require.Error(t, err)
	source.DisabledChannels = map[string]bool{"swing_i2i": true, "friend": true}
	source.SwingRecall = nil
	for _, channel := range []string{"swing_i2i", "friend"} {
		docs, err = source.Recall(ctx, c, Broadcast, channel, 200)
		require.NoError(t, err)
		require.Empty(t, docs, "disabled channel must not touch dependencies")
	}
}

func TestUserRecallSharedAcrossNeedsAndCappedAfterMerge(t *testing.T) {
	e, s, store := engineFixture()
	e.FriendFeedEnabled = true
	e.SourceLimits = []SourceLimit{{Source: "friend", Numerator: 1, Denominator: 2}}
	store.active = []need.Snapshot{capturedFixture(1, Broadcast), capturedFixture(2, Broadcast)}
	for i := range store.active {
		editCaptured(t, &store.active[i], func(in *need.Input) { in.Constraints.Lang = []string{"zh"} })
	}
	s.calls = map[string]int{}
	s.channels = map[string][]Document{}
	for id := int64(1); id <= 6; id++ {
		d := Document{Ref: SourceRef{Broadcast, id}, AuthorID: 20, Version: "1", Active: true, Visible: true, GroupID: id, Lexical: 0}
		s.channels["friend"] = append(s.channels["friend"], d)
	}
	// Non-friend items backfill the user's quota after merging both Needs.
	for id := int64(7); id <= 12; id++ {
		s.docs = append(s.docs, Document{Ref: SourceRef{Broadcast, id}, AuthorID: 20, Version: "1", Active: true, Visible: true, GroupID: id, Lexical: 10, Slots: searchindex.Slots{Lang: []string{"zh"}}})
	}
	x, err := e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast}, Limit: 4}, Recommendation, 100)
	require.NoError(t, err)
	require.Len(t, x.Candidates, 4)
	require.Equal(t, 1, s.calls["friend"])
	require.Equal(t, 1, s.calls["swing_i2i"])
	friends := 0
	for _, c := range x.Candidates {
		if c.Document.RecallSources().Has(recallsource.Friend) {
			friends++
			require.Equal(t, float64(1), c.Score.Features["friend_relevance_bypass"])
		}
	}
	require.LessOrEqual(t, friends, 2, "the user has one cap, not one per Need")
	// Force only friend candidates to make the cap observable even without backfill.
	s.docs = nil
	x, err = e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast}, Limit: 4}, Recommendation, 100)
	require.NoError(t, err)
	require.Len(t, x.Candidates, 2)
	for _, c := range x.Candidates {
		require.Equal(t, "friend", c.Context.Origin)
		require.Nil(t, c.Context.CapturedNeed)
		require.Zero(t, c.Context.NeedID())
		require.Empty(t, c.Context.Filters.Lang, "a Need language restriction must not leak into the friend lane")
	}
	before := s.calls["friend"]
	_, err = e.Execute(context.Background(), 1, Request{Query: "design", SourceKinds: []Kind{Broadcast}}, Search, 100)
	require.NoError(t, err)
	require.Equal(t, before, s.calls["friend"], "query search must not add friend recall")
}

func TestFriendRecallStillPassesHardFiltersAndDedup(t *testing.T) {
	for _, reject := range []string{"blocked", "expired", "self", "language", "seen"} {
		t.Run(reject, func(t *testing.T) {
			e, s, _ := engineFixture()
			e.FriendFeedEnabled = true
			s.owner.Clauses = []string{"design"}
			d := Document{Ref: SourceRef{Broadcast, 9}, AuthorID: 2, Version: "1", Active: true, Visible: true}
			r := Request{SourceKinds: []Kind{Broadcast}}
			switch reject {
			case "blocked":
				d.Blocked = true
			case "expired":
				d.ExpiresAt = 99
			case "self":
				d.AuthorID = 1
			case "language":
				r.Filters.Lang = []string{"zh"}
			case "seen":
				s.seen[d.Ref.Key()] = true
			}
			s.channels = map[string][]Document{"friend": {d}}
			x, err := e.Execute(context.Background(), 1, r, Recommendation, 100)
			require.NoError(t, err)
			require.Empty(t, x.Candidates)
		})
	}
}

func TestUserSourceAttributionSurvivesWinningNeed(t *testing.T) {
	high := Candidate{Context: Context{ID: 1, Priority: 1}, Document: Document{Ref: SourceRef{Broadcast, 9}, Channels: []string{"lexical"}}, Score: Score{Eligible: true, Value: 1}}
	low := high
	low.Context.ID = 2
	low.Context.Priority = 0
	low.Document.Channels = []string{"friend"}
	merged := Merge([]Candidate{high, low}, []Kind{Broadcast}, Recommendation, 10)
	require.Len(t, merged, 1)
	require.Contains(t, merged[0].Document.Channels, "friend")
	page, _ := SelectRecommendationPage(merged, 1, []SourceLimit{{Source: "friend", Numerator: 1, Denominator: 2}})
	require.Empty(t, page, "winning non-friend attribution cannot bypass the user cap")
}

func TestFriendLaneIsOptionalAndSwingFailureIsPartial(t *testing.T) {
	e, s, _ := engineFixture()
	s.channels = map[string][]Document{"friend": {{Ref: SourceRef{Broadcast, 9}, AuthorID: 2, Version: "1", Active: true, Visible: true}}}
	s.calls = map[string]int{}
	r := Request{SourceKinds: []Kind{Broadcast}}
	x, err := e.Execute(context.Background(), 1, r, Recommendation, 100)
	require.NoError(t, err)
	require.Empty(t, x.Candidates)
	require.Zero(t, s.calls["friend"])
	e.FriendFeedEnabled = true
	s.channelErrors = map[string]error{"swing_i2i": fmt.Errorf("missing active generation")}
	x, err = e.Execute(context.Background(), 1, r, Recommendation, 100)
	require.NoError(t, err)
	require.Len(t, x.Candidates, 1)
	require.Contains(t, x.PartialReasons, "broadcast:swing_i2i_unavailable")
	before := s.calls["friend"]
	_, err = e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Agent}}, Recommendation, 100)
	require.NoError(t, err)
	require.Equal(t, before, s.calls["friend"])
}
