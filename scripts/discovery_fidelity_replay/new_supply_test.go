package main

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"eigenflux_server/pkg/featureindex"
	"eigenflux_server/rpc/sort/discovery"
	"github.com/stretchr/testify/require"
)

func supplyFixture() (*snapshot, supplyItem) {
	s := &snapshot{Owner: 7, At: 2000, Serving: &servingEvidence{Format: servingCaptureFormat}, Classes: map[int64]string{1: "ugc"}, Seen: map[string]bool{}}
	d := discovery.Document{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 2}, AuthorID: 9, GroupID: 2, Text: "body\nsummary", Active: true, Visible: true, FreshAt: 1000, Quality: 0.8}
	d.Version = featureindex.BroadcastContentHash(d.Ref.ID, d.AuthorID, d.Text, d.Slots)
	return s, supplyItem{Document: storeDocs([]discovery.Document{d})[0], Evidence: itemEvidence{ItemID: 2, AuthorID: 9, Content: "body", Summary: "summary"}, Vector: []float32{1, 0}, Owners: map[int64]supplyOwner{7: {Seen: true, Blocked: true}}, Provenance: "private extraction and grouping evidence"}
}
func TestNewSupplyRejectsIncompleteEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*snapshot, *supplyItem)
	}{
		{"owner", func(s *snapshot, x *supplyItem) { delete(x.Owners, 7) }},
		{"collision", func(s *snapshot, x *supplyItem) { s.Inventory = []storedDocument{x.Document} }},
		{"fingerprint", func(s *snapshot, x *supplyItem) { x.Evidence.Content = "changed" }},
		{"group", func(s *snapshot, x *supplyItem) { x.Document.GroupID = 0 }},
		{"future", func(s *snapshot, x *supplyItem) { x.Document.FreshAt = 3000 }},
		{"nonfinite", func(s *snapshot, x *supplyItem) { x.Vector = []float32{float32(math.Inf(1)), 1} }},
		{"legacy", func(s *snapshot, x *supplyItem) { s.Serving = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, x := supplyFixture()
			tt.mutate(s, &x)
			dir := t.TempDir()
			b, err := json.Marshal([]supplyItem{x})
			if tt.name == "nonfinite" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "new-supply.json"), b, 0600))
			_, err = loadNewSupply(dir, []*snapshot{s})
			require.Error(t, err)
		})
	}
}
func TestSupplyArmPreservesOwnerFactsAndBaseline(t *testing.T) {
	s, x := supplyFixture()
	b, _ := json.Marshal(s)
	candidate := supplySnapshot(s, []supplyItem{x})
	require.Len(t, candidate.Inventory, 1)
	require.True(t, candidate.Inventory[0].Blocked)
	require.True(t, candidate.Seen[x.Document.Ref.Key()])
	require.Equal(t, "pgc", candidate.Classes[2])
	candidate.Classes[1] = "pgc"
	candidate.Seen["unrelated"] = true
	after, _ := json.Marshal(s)
	require.Equal(t, string(b), string(after))
}
func TestServingCosineMissingIsNotZero(t *testing.T) {
	s, _ := supplyFixture()
	key := vectorKey([]float32{1, 0})
	s.Serving.Cosines = map[string]map[int64]float64{key: {1: 0.8}}
	s.Serving.CosineRead = map[string]map[int64]bool{key: {1: true, 2: true}}
	source := &servingSource{snap: s, mu: &sync.Mutex{}}
	got, err := source.Cosine(context.Background(), []float32{1, 0}, []int64{1, 2})
	require.NoError(t, err)
	require.Equal(t, map[int64]float64{1: 0.8}, got)
	_, err = source.Cosine(context.Background(), []float32{1, 0}, []int64{3})
	require.Error(t, err)
	require.NotEmpty(t, s.CaptureError)
}
func TestNewSupplyCompetesAtDenseCutoff(t *testing.T) {
	s, x := supplyFixture()
	d := restoreDocs([]storedDocument{x.Document})[0]
	weak := 0.55
	old := d
	old.Ref.ID = 1
	old.DenseScore = &weak
	p := &poolSource{snap: s, docs: map[int64]discovery.Document{2: d}, vectors: map[int64][]float32{2: x.Vector}}
	got, err := p.dense(discovery.Context{State: "active", Kinds: []discovery.Kind{discovery.Broadcast}, Vector: []float32{1, 0}}, []discovery.Document{old}, 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, int64(2), got[0].Ref.ID)
	p.vectors[2] = []float32{-1, 0}
	got, err = p.dense(discovery.Context{State: "active", Kinds: []discovery.Kind{discovery.Broadcast}, Vector: []float32{1, 0}}, []discovery.Document{old}, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), got[0].Ref.ID)
}

func TestNewSupplyAcceptsCompleteEvidence(t *testing.T) {
	s, x := supplyFixture()
	dir := t.TempDir()
	b, err := json.Marshal([]supplyItem{x})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new-supply.json"), b, 0600))
	got, err := loadNewSupply(dir, []*snapshot{s})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, restoreDocs([]storedDocument{x.Document}), restoreDocs([]storedDocument{got[0].Document}))
	require.Equal(t, x.Owners, got[0].Owners)
}
func TestRefreshedNewRecallChangesMembershipWithoutForcingDelivery(t *testing.T) {
	s, x := supplyFixture()
	c := discovery.Context{ID: 8, State: "active", OwnerID: 7}
	prior := discovery.Document{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 1}}
	s.Channels = map[string]channel{channelKey(c, discovery.Broadcast, "new_recall"): {ContextID: c.ID, Kind: discovery.Broadcast, Name: "new_recall", Limit: 1, Documents: storeDocs([]discovery.Document{prior})}}
	p := &poolSource{snap: s, docs: map[int64]discovery.Document{2: restoreDocs([]storedDocument{x.Document})[0]}}
	src := &supplySource{poolSource: p, additions: map[int64][]float32{2: x.Vector}}
	before, err := src.Recall(context.Background(), c, discovery.Broadcast, "new_recall", 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), before[0].Ref.ID)
	src.refreshedNewRecall = true
	after, err := src.Recall(context.Background(), c, discovery.Broadcast, "new_recall", 1)
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.Equal(t, int64(2), after[0].Ref.ID)
	require.False(t, after[0].Active) // ID-only recall still requires authoritative hydration.
	require.Equal(t, int64(1), s.Channels[channelKey(c, discovery.Broadcast, "new_recall")].Documents[0].Ref.ID)
}

type contextOnlyHydration struct{ discovery.Sources }

func (contextOnlyHydration) Hydrate(ctx context.Context, _ int64, _ discovery.Mode, _ []discovery.Document) ([]discovery.Document, error) {
	return nil, ctx.Err()
}
func TestOfflineHydrationUsesCaptureDeadlineOnlyWhileCollecting(t *testing.T) {
	optional, cancelOptional := context.WithCancel(context.Background())
	cancelOptional()
	capture, cancelCapture := context.WithCancel(context.Background())
	defer cancelCapture()
	s := &servingSource{Sources: contextOnlyHydration{}, captureContext: capture, live: &discovery.Source{}}
	_, err := s.Hydrate(optional, 1, discovery.Recommendation, nil)
	require.NoError(t, err)
	cancelCapture()
	_, err = s.Hydrate(context.Background(), 1, discovery.Recommendation, nil)
	require.ErrorIs(t, err, context.Canceled)
	s.live = nil
	_, err = s.Hydrate(optional, 1, discovery.Recommendation, nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestNewSupplyPreservesCapturedDenseTies(t *testing.T) {
	score := 0.7
	docs := []discovery.Document{{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 9}, DenseScore: &score}, {Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 1}, DenseScore: &score}, {Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 7}}, {Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 2}}}
	p := &poolSource{}
	got, err := p.dense(discovery.Context{}, docs, 4)
	require.NoError(t, err)
	require.Equal(t, docs, got)
}

func TestNewSupplyPreservesDuplicateRecallVersions(t *testing.T) {
	score := 0.7
	a := discovery.Document{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 1}, Version: "stale", DenseScore: &score}
	b := a
	b.Version = "current"
	docs := []discovery.Document{a, b}
	got, err := (&poolSource{}).dense(discovery.Context{}, docs, 2)
	require.NoError(t, err)
	require.Equal(t, docs, got)
}
