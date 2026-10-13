package discovery

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReplayContextsUsesFullEligibilityAndSeenPipeline(t *testing.T) {
	e, s, _ := engineFixture()
	e.Compiler = nil
	e.Needs = nil
	e.IDs = nil
	e.FriendFeedEnabled = true
	s.calls = map[string]int{}
	for _, id := range []int64{10, 11, 12, 13} {
		s.docs = append(s.docs, Document{Ref: SourceRef{Broadcast, id}, AuthorID: 2, Active: true, Visible: true, Version: "v1", Lexical: 10, Quality: 1, FreshAt: 100})
	}
	s.seen["broadcast:10"] = true
	s.docs[1].Blocked = true
	s.docs[2].Lexical = 0
	s.docs[2].Quality = 0
	s.docs[2].FreshAt = 1
	c := Context{ID: 42, OwnerID: 1, State: "active", Origin: "agent_context", Query: "design", Kinds: []Kind{Broadcast}}
	result, err := e.ReplayContexts(context.Background(), 1, []Context{c}, []Kind{Broadcast}, 20, 100)
	require.NoError(t, err)
	require.Len(t, result.Candidates, 1)
	require.Equal(t, int64(13), result.Candidates[0].Document.Ref.ID)
	require.Equal(t, 1, s.calls["lexical"])
	require.Equal(t, 1, s.calls["new_ugc_recall"])
	require.Equal(t, 1, s.calls["swing_i2i"])
	require.Zero(t, s.calls["friend"], "replay must not manufacture a second friend lane")
	require.Nil(t, e.frozenContexts, "original engine must not retain replay contexts")
}
func TestReplayRejectsForeignInactiveOrOutOfScopeContext(t *testing.T) {
	for _, c := range []Context{
		{ID: 1, OwnerID: 2, State: "active", Kinds: []Kind{Broadcast}},
		{ID: 1, OwnerID: 1, State: "inactive", Kinds: []Kind{Broadcast}},
		{ID: 1, OwnerID: 1, State: "active", Kinds: []Kind{Commission}},
	} {
		e, _, _ := engineFixture()
		_, err := e.ReplayContexts(context.Background(), 1, []Context{c}, []Kind{Broadcast}, 20, 100)
		require.Error(t, err)
	}
}
