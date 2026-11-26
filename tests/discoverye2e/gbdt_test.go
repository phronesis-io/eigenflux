package discoverye2e

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eigenflux_server/rpc/sort/discovery"
	"eigenflux_server/rpc/sort/discoverylr"
	"github.com/stretchr/testify/require"
)

func TestDiscoveryGBDTRecommendation(t *testing.T) {
	raw, err := os.ReadFile("../../rpc/sort/discoverylr/testdata/discovery_snapshot_v1.json")
	require.NoError(t, err)
	var fixture struct{ Model json.RawMessage }
	require.NoError(t, json.Unmarshal(raw, &fixture))
	path := filepath.Join(t.TempDir(), "model.json")
	require.NoError(t, os.WriteFile(path, fixture.Model, 0600))
	s := startStack(t, map[string]string{"DISCOVERY_GBDT_BPS": "10000", "DISCOVERY_GBDT_MODEL_PATH": path})
	s.saved(t, "broadcast")
	response := s.recommend(t, discovery.Request{SourceKinds: []discovery.Kind{discovery.Broadcast}, Limit: 10}, "gbdt")
	require.NotEmpty(t, response.Items)
	s.waitSamples(t, response.ImpressionID, len(response.Items))
	for _, item := range response.Items {
		require.Equal(t, "gbdt", item.Match["scorer_type"])
		var raw string
		require.NoError(t, s.db.Table("replay_logs").Select("item_features").Where("impression_id=? AND item_id=?", response.ImpressionID, item.Ref.ID).Scan(&raw).Error)
		c := decode[struct{ Search discovery.Candidate }](t, []byte(raw)).Search
		require.NotNil(t, c.LR)
		require.Equal(t, discoverylr.SnapshotContract, c.LR.Contract)
		require.Equal(t, "gbdt_fixture", c.LR.ModelVersion)
		logit := -0.75
		if strings.TrimSpace(c.Context.Query) != "" {
			logit = 0.5
		}
		require.InDelta(t, 1/(1+math.Exp(-logit)), c.LR.Probability, 1e-12)
	}
}
