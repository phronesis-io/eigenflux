package discoverylr

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGBDTRejectsBadTreesAndPinsVersion(t *testing.T) {
	raw, err := os.ReadFile("testdata/discovery_snapshot_v1.json")
	require.NoError(t, err)
	var f struct{ Model model }
	require.NoError(t, json.Unmarshal(raw, &f))
	path := filepath.Join(t.TempDir(), "model.json")
	save := func() {
		b, err := json.Marshal(f.Model)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, b, 0600))
	}
	save()
	disabled := NewSnapshot(false, path, time.Hour)
	require.Nil(t, disabled.Pin())
	disabled.Close()
	m := NewSnapshot(true, path, time.Hour)
	defer m.Close()
	pinned := m.Pin()
	require.NotNil(t, pinned)
	f.Model.Version = "new"
	save()
	m.reload(path)
	scores, ok := pinned.ScoreBatch([][]float64{f.Model.SelfTests[0].Features})
	require.True(t, ok)
	require.Equal(t, "gbdt_fixture", scores[0].ModelVersion)
	require.Equal(t, "new", m.current.Load().Version)
	for _, bad := range []string{"cycle", "range", "feature", "arrays", "unreachable", "duplicate"} {
		t.Run(bad, func(t *testing.T) {
			require.NoError(t, json.Unmarshal(raw, &f))
			tree := &f.Model.Trees[0]
			switch bad {
			case "cycle":
				tree.Left[0] = 0
			case "range":
				tree.Left[0] = len(tree.Feature)
			case "feature":
				tree.Feature[0] = len(SnapshotFeatureNames)
			case "arrays":
				tree.Threshold = nil
			case "unreachable":
				tree.Feature[0], tree.Left[0], tree.Right[0] = -1, -1, -1
			case "duplicate":
				tree.Right[0] = tree.Left[0]
			}
			save()
			_, err := load(path, SnapshotContract)
			require.Error(t, err)
			m.reload(path)
			require.Equal(t, "new", m.current.Load().Version)
		})
	}
}
