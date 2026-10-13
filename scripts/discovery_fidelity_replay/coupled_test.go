package main

import (
	"eigenflux_server/rpc/sort/discovery"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDenseCounterfactualCanPromotePreviouslyUnrecalledCandidate(t *testing.T) {
	c := discovery.Context{ID: 1, OwnerID: 2, State: "active", Kinds: []discovery.Kind{discovery.Broadcast}, Vector: []float32{1, 0}}
	old := discovery.Document{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 3}, AuthorID: 7}
	score := .7
	old.DenseScore = &score
	fresh := old
	fresh.Ref.ID = 4
	p := poolSource{docs: map[int64]discovery.Document{4: fresh}, vectors: map[int64][]float32{4: {1, 0}}, dropped: map[int64]bool{}}
	got, e := p.dense(c, []discovery.Document{old}, 1)
	require.NoError(t, e)
	require.Len(t, got, 1)
	require.Equal(t, int64(4), got[0].Ref.ID)
	c.Filters.ExcludeAuthors = []string{"7"}
	got, e = p.dense(c, []discovery.Document{old}, 1)
	require.NoError(t, e)
	require.Equal(t, int64(3), got[0].Ref.ID)
	_, e = cosineScore([]float32{1}, []float32{1, 0})
	require.Error(t, e)
}
func TestDenseRetainsUnchangedVectorWhenOnlySummaryChanged(t *testing.T) {
	c := discovery.Context{ID: 1, State: "active", Kinds: []discovery.Kind{discovery.Broadcast}, Vector: []float32{1, 0}}
	score := .8
	old := discovery.Document{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 3}, AuthorID: 7, Version: "old", DenseScore: &score}
	fresh := old
	fresh.Version = "new"
	fresh.Preview = "grounded summary"
	p := poolSource{changed: map[int64]bool{3: true}, docs: map[int64]discovery.Document{3: fresh}, dropped: map[int64]bool{}}
	got, e := p.dense(c, []discovery.Document{old}, 20)
	require.NoError(t, e)
	require.Equal(t, "new", got[0].Version)
	require.Equal(t, &score, got[0].DenseScore)
}
