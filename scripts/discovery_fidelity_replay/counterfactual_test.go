package main

import (
	"context"
	"testing"

	"eigenflux_server/rpc/sort/discovery"
	"github.com/stretchr/testify/require"
)

func TestCounterfactualUpdatesPresentationWithoutChangingRankingMetadata(t *testing.T) {
	original := discovery.Document{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 7}, AuthorID: 9, Version: "old", Preview: "clinical trial", Text: "source\nclinical trial", Quality: .8, GroupID: 77, FreshAt: 123, Active: true, Visible: true, Lexical: 3.5}
	corrected := original
	corrected.Version = "new"
	corrected.Preview = "trial"
	corrected.Text = "source\ntrial"
	s := &snapshot{Channels: map[string]channel{channelKey(discovery.Context{ID: 1}, discovery.Broadcast, "dense"): {ContextID: 1, Kind: discovery.Broadcast, Name: "dense", Limit: 80, Documents: storeDocs([]discovery.Document{original})}}, Seen: map[string]bool{original.Ref.Key(): false}}
	p := &poolSource{changed: map[int64]bool{7: true}, snap: s, docs: map[int64]discovery.Document{7: corrected}, dropped: map[int64]bool{}}
	got, err := p.Recall(context.Background(), discovery.Context{ID: 1}, discovery.Broadcast, "dense", 80)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, corrected, got[0])
	require.Equal(t, original, restoreDocs(s.Channels[channelKey(discovery.Context{ID: 1}, discovery.Broadcast, "dense")].Documents)[0])
	p.dropped[7] = true
	got, err = p.Recall(context.Background(), discovery.Context{ID: 1}, discovery.Broadcast, "dense", 80)
	require.NoError(t, err)
	require.Empty(t, got)
	got, err = p.Hydrate(context.Background(), 1, discovery.Recommendation, []discovery.Document{original})
	require.NoError(t, err)
	require.Empty(t, got)
}
func TestAuditInputHashMatchesPythonJSONAndBindsAllInputs(t *testing.T) {
	item := itemEvidence{Content: "source", Notes: "", Summary: "summary"}
	require.Equal(t, "98ec1a1bba115278100d6d2d3c327a02d555ab46d9f1bb49bc79f5d8b93950eb", auditInputHash(item))
	prior := auditInputHash(item)
	item.Notes = "new provenance"
	require.NotEqual(t, prior, auditInputHash(item))
}

func TestAuditHashPreservesLiteralEscapesAndUnicodeSeparators(t *testing.T) {
	require.Equal(t, "fe808e2701662605dee3bf1917ebf6baebd9f9f0ded605f6f7250245634805e6", auditInputHash(itemEvidence{Content: "中文<>&\\u2028\u2028\u2029"}))
}

func TestIndexExpiryNormalizesStoredSQLDates(t *testing.T) {
	require.Equal(t, "2026-10-12T23:00:00Z", indexExpiry("2026-10-12 23:00:00"))
	require.Equal(t, "", indexExpiry("invalid"))
	require.Equal(t, "2026-10-12T00:00:00Z", indexExpiry("2026-10-12"))
}
func TestCounterfactualDoesNotHealUnrelatedStaleRecallEvidence(t *testing.T) {
	d := discovery.Document{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 7}, Version: "stale"}
	fresh := d
	fresh.Version = "authority"
	s := &snapshot{Channels: map[string]channel{channelKey(discovery.Context{ID: 1}, discovery.Broadcast, "dense"): {ContextID: 1, Kind: discovery.Broadcast, Name: "dense", Limit: 80, Documents: storeDocs([]discovery.Document{d})}}}
	p := poolSource{snap: s, docs: map[int64]discovery.Document{7: fresh}, stale: map[int64]bool{7: true}}
	got, e := p.Recall(context.Background(), discovery.Context{ID: 1}, discovery.Broadcast, "dense", 80)
	require.NoError(t, e)
	require.Equal(t, "stale", got[0].Version)
}
