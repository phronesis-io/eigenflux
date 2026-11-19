package main

import (
	"testing"

	"eigenflux_server/pkg/need"
	"eigenflux_server/rpc/sort/discovery"
	"github.com/stretchr/testify/require"
)

func TestNeedPageIncludesSilentNeedsAndReportsMissingCosine(t *testing.T) {
	contexts := []discovery.Context{{ID: 1, CapturedNeed: &need.Snapshot{}}, {ID: 2, CapturedNeed: &need.Snapshot{}}, {ID: 3, CapturedNeed: &need.Snapshot{}}, {ID: 4}}
	candidate := func(i int, features map[string]float64) discovery.Candidate {
		return discovery.Candidate{Context: contexts[i], Document: discovery.Document{Ref: discovery.SourceRef{Type: discovery.Broadcast}}, Score: discovery.Score{Features: features}}
	}
	strong := candidate(0, map[string]float64{"need_cosine": .4})
	weak := candidate(1, map[string]float64{"need_cosine": .39})
	page := []discovery.Candidate{strong, strong, weak, candidate(0, nil), candidate(3, nil)}
	got := summarizeNeedPage(contexts, page, .4)
	require.Equal(t, needPage{Counts: map[int64]int{1: 3, 2: 1, 3: 0}, NeedItems: 4, AbsentNeeds: 1, BelowFloor: 1, MissingCosine: 1, TopShare: .75, BelowFloorShare: .25}, got)
	require.Equal(t, 3, summarizeNeedPage(contexts, page, .5).BelowFloor, "the configured floor is applied")
	got = summarizeNeedPage(contexts, nil, .4)
	require.Zero(t, got.TopShare)
	require.Equal(t, 3, got.AbsentNeeds)
}
