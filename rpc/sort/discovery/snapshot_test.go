package discovery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"eigenflux_server/rpc/sort/discoverylr"
	"github.com/stretchr/testify/require"
)

type snapshotCase struct {
	Name        string
	Candidate   Candidate
	Features    []float64
	Probability float64
	Error       string
}

func snapshotFixture(t *testing.T) (string, []snapshotCase) {
	t.Helper()
	raw, err := os.ReadFile("../discoverylr/testdata/discovery_snapshot_v1.json")
	require.NoError(t, err)
	var data struct {
		Model json.RawMessage
		Cases []snapshotCase
	}
	require.NoError(t, json.Unmarshal(raw, &data))
	path := filepath.Join(t.TempDir(), "model.json")
	require.NoError(t, os.WriteFile(path, data.Model, 0600))
	return path, data.Cases
}

func TestSnapshotPythonParity(t *testing.T) {
	path, cases := snapshotFixture(t)
	m := discoverylr.NewSnapshot(true, path, time.Hour)
	defer m.Close()
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			v, ok := snapshotVector(tc.Candidate)
			if tc.Error != "" {
				require.False(t, ok)
				return
			}
			require.True(t, ok)
			require.Len(t, v, 29)
			require.InDeltaSlice(t, tc.Features, v, 1e-14)
			scores, ok := m.ScoreBatch([][]float64{v})
			require.True(t, ok)
			require.InDelta(t, tc.Probability, scores[0].Probability, 1e-12)
		})
	}
}

func TestSnapshotRankingAndWholePoolFallback(t *testing.T) {
	path, _ := snapshotFixture(t)
	for _, tc := range []struct {
		name     string
		bps      int
		model    string
		badDoc   bool
		contract string
	}{
		{"default", 0, path, false, discoverylr.RatingContract},
		{"treatment", 10000, path, false, discoverylr.SnapshotContract},
		{"missing model", 10000, path + ".missing", false, discoverylr.RatingContract},
		{"one invalid candidate", 10000, path, true, discoverylr.RatingContract},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, s, _, rating, _, pgc := ratingFixture()
			e.GBDTBPS = tc.bps
			e.GBDT = discoverylr.NewSnapshot(tc.bps > 0, tc.model, time.Hour)
			defer e.GBDT.Close()
			s.docs = []Document{profileDoc(1, 5, 1), profileDoc(2, 5, 1)}
			if tc.badDoc {
				s.docs[1].FreshAt = 0
			}
			s.cosine[1], s.cosine[2] = .2, .8
			x := runProfile(t, e)
			require.Len(t, x.Candidates, 2)
			for _, c := range x.Candidates {
				require.NotNil(t, c.SemanticRank)
				require.NotNil(t, c.LR)
				require.Equal(t, tc.contract, c.LR.Contract)
			}
			if tc.contract == discoverylr.SnapshotContract {
				require.Zero(t, rating.calls)
				require.Zero(t, pgc.calls)
				// Equal model scores follow the stable ID tie-break even across semantic tiers.
				require.EqualValues(t, 1, x.Candidates[0].Document.Ref.ID)
			} else {
				require.Positive(t, rating.calls)
			}
			if tc.bps == 0 {
				require.NotContains(t, x.Experiments, "discovery-gbdt-v1")
			} else {
				require.Equal(t, "treatment", x.Experiments["discovery-gbdt-v1"])
			}
		})
	}
}

func TestSnapshotSkipsSearchAndMixedRecommendations(t *testing.T) {
	path, _ := snapshotFixture(t)
	e, s, _, _, _, _ := ratingFixture()
	e.GBDTBPS = 10000
	e.GBDT = discoverylr.NewSnapshot(true, path, time.Hour)
	defer e.GBDT.Close()
	s.docs = []Document{profileDoc(1, 5, 1)}
	for _, tc := range []struct {
		request Request
		mode    Mode
	}{
		{Request{SourceKinds: []Kind{Broadcast, Commission}, Limit: 20}, Recommendation},
		{Request{Query: "battery", SourceKinds: []Kind{Broadcast}, Limit: 20}, Search},
	} {
		x, err := e.Execute(context.Background(), 1, tc.request, tc.mode, testNow)
		require.NoError(t, err)
		require.NotContains(t, x.Experiments, "discovery-gbdt-v1")
		for _, c := range x.Candidates {
			if c.LR != nil {
				require.NotEqual(t, discoverylr.SnapshotContract, c.LR.Contract)
			}
		}
	}
}
