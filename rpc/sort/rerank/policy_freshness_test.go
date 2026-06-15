package rerank

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"eigenflux_server/rpc/sort/rank"
)

type testItemFreshnessSource struct {
	broadcastType string
	updatedAt     time.Time
}

func (s testItemFreshnessSource) ItemFreshnessFields() (string, time.Time) {
	return s.broadcastType, s.updatedAt
}

func TestFreshnessPolicy_DropsStaleAlert(t *testing.T) {
	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	fresh := rank.NewCandidate(1, rank.CandidateItem, 0.9, nil, testItemFreshnessSource{
		broadcastType: "alert",
		updatedAt:     now.Add(-5 * time.Hour),
	})
	stale := rank.NewCandidate(2, rank.CandidateItem, 0.8, nil, testItemFreshnessSource{
		broadcastType: "alert",
		updatedAt:     now.Add(-7 * time.Hour),
	})
	info := rank.NewCandidate(3, rank.CandidateItem, 0.7, nil, testItemFreshnessSource{
		broadcastType: "info",
		updatedAt:     now.Add(-7 * time.Hour),
	})

	policy := &FreshnessPolicy{
		Now: func() time.Time { return now },
		ItemRules: []ItemFreshnessRule{
			{BroadcastType: "alert", MaxAge: 6 * time.Hour, Action: "drop"},
		},
	}
	out := policy.Apply([]rank.Candidate{fresh, stale, info})

	require.Len(t, out, 2)
	assert.Equal(t, int64(1), out[0].ID())
	assert.Equal(t, int64(3), out[1].ID())
	assert.Contains(t, stale.Reasons(), "freshness:drop")
}

func TestFreshnessPolicy_IgnoresServicesAndUnsupportedSources(t *testing.T) {
	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	service := rank.NewCandidate(1, rank.CandidateService, 0.9, nil, testItemFreshnessSource{
		broadcastType: "alert",
		updatedAt:     now.Add(-7 * time.Hour),
	})
	unknown := rank.NewCandidate(2, rank.CandidateItem, 0.8, nil, nil)

	policy := &FreshnessPolicy{
		Now: func() time.Time { return now },
		ItemRules: []ItemFreshnessRule{
			{BroadcastType: "alert", MaxAge: 6 * time.Hour, Action: "drop"},
		},
	}
	out := policy.Apply([]rank.Candidate{service, unknown})

	require.Len(t, out, 2)
	assert.Equal(t, int64(1), out[0].ID())
	assert.Equal(t, int64(2), out[1].ID())
}
