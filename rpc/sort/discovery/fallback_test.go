package discovery

import (
	"context"
	"testing"

	"eigenflux_server/pkg/need"
	"github.com/stretchr/testify/require"
)

func TestRecommendationFallbackIsPerKind(t *testing.T) {
	for _, kind := range AllKinds {
		t.Run(string(kind), func(t *testing.T) {
			e, source, store := engineFixture()
			source.owner = OwnerContext{Revision: "card:1", Clauses: []string{"design"}}
			n := capturedFixture(7, kind)
			editCaptured(t, &n, func(in *need.Input) { in.Constraints.Lang = []string{"zh"} })
			store.active = []need.Snapshot{n}
			for _, k := range AllKinds {
				source.docs = append(source.docs, baseDoc(k))
			}
			x, err := e.Execute(context.Background(), 1, Request{Limit: 10}, Recommendation, 100)
			require.NoError(t, err)
			require.Equal(t, "missing_kind_needs", x.FallbackReason)
			require.Len(t, x.Candidates, 2)
			for _, c := range x.Candidates {
				require.NotEqual(t, kind, c.Document.Ref.Type, "unsatisfied captured constraints were bypassed")
				require.Equal(t, "agent_context", c.Context.Origin)
				require.NotContains(t, c.Context.Kinds, kind)
			}
			require.Equal(t, int64(7), x.Contexts[0].NeedID())
		})
	}
}

func TestRecommendationFallbackEmptyContextAndExplicitSelection(t *testing.T) {
	e, source, store := engineFixture()
	n := capturedFixture(7, Agent)
	store.active = []need.Snapshot{n}
	store.needs[7] = n
	source.docs = []Document{baseDoc(Agent), baseDoc(Broadcast), baseDoc(Commission)}
	x, err := e.Execute(context.Background(), 1, Request{Limit: 10}, Recommendation, 100)
	require.NoError(t, err)
	require.Len(t, x.Candidates, 2)
	require.Equal(t, "empty_agent_context", x.FallbackReason)
	for _, c := range x.Candidates {
		require.NotEqual(t, Commission, c.Document.Ref.Type)
	}
	require.Equal(t, []Kind{Broadcast}, x.Contexts[1].Kinds)
	// Explicit selection remains restricted to the selected Need, even with a
	// wider source_kinds default. It never opts into unrelated fallback.
	x, err = e.Execute(context.Background(), 1, Request{NeedIDs: []string{"7"}}, Recommendation, 100)
	require.NoError(t, err)
	require.Len(t, x.Contexts, 1)
	require.Len(t, x.Candidates, 1)
	require.Empty(t, x.FallbackReason)
	// The selected Need wins a small overall limit before fallback presentation.
	x, err = e.Execute(context.Background(), 1, Request{Limit: 1}, Recommendation, 100)
	require.NoError(t, err)
	require.Len(t, x.Candidates, 1)
	require.Equal(t, int64(7), x.Candidates[0].Context.NeedID())
}
