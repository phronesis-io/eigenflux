package discoverye2e

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"eigenflux_server/rpc/sort/discovery"
	"github.com/stretchr/testify/require"
)

func TestDiscoveryLRRecommendation(t *testing.T) {
	raw, err := os.ReadFile("../../rpc/sort/discoverylr/testdata/discovery_lr_v1.json")
	require.NoError(t, err)
	var fixture struct {
		Model json.RawMessage `json:"model"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))
	path := filepath.Join(t.TempDir(), "model.json")
	require.NoError(t, os.WriteFile(path, fixture.Model, 0600))
	s := startStack(t, map[string]string{"DISCOVERY_LR_ENABLED": "true", "DISCOVERY_LR_MODEL_PATH": path})
	for _, kind := range discovery.AllKinds {
		s.saved(t, string(kind))
	}
	var model struct {
		Version      string    `json:"model_version"`
		Coefficients []float64 `json:"coefficients"`
		Intercept    float64   `json:"intercept"`
	}
	require.NoError(t, json.Unmarshal(fixture.Model, &model))
	for _, mode := range []discovery.Mode{discovery.Search, discovery.Recommendation} {
		var response discovery.Response
		if mode == discovery.Search {
			response = s.search(t, discovery.Request{Query: "landing page design", Limit: 10}, "lr-search")
		} else {
			response = s.recommend(t, discovery.Request{Limit: 10}, "lr-recommend")
		}
		require.NotEmpty(t, response.Items)
		s.waitSamples(t, response.ImpressionID, len(response.Items))
		var rows []struct{ ItemFeatures string }
		require.NoError(t, s.db.Raw("SELECT item_features::text FROM replay_logs WHERE impression_id=? AND agent_id=? ORDER BY position", response.ImpressionID, s.owner).Scan(&rows).Error)
		learned := 0
		for _, row := range rows {
			var snapshot struct {
				Search discovery.Candidate `json:"search"`
			}
			require.NoError(t, json.Unmarshal([]byte(row.ItemFeatures), &snapshot))
			c := snapshot.Search
			if mode == discovery.Search || c.Document.Ref.Type != discovery.Broadcast {
				require.Nil(t, c.LR)
				continue
			}
			learned++
			require.NotNil(t, c.LR)
			require.Equal(t, model.Version, c.LR.ModelVersion)
			require.Equal(t, "rules", c.Score.ScorerType, "frozen rule evidence must survive learned ranking and delivery")
			values := []float64{c.Score.Features["lexical"], c.Score.Features["semantic"], c.Score.Features["freshness"], c.Score.Features["quality"], c.Score.Relevance, c.Score.Value, 0, 0, 0}
			for _, missing := range c.Score.Missing {
				if missing == "semantic" {
					values[6] = 1
				}
			}
			if c.Context.Origin == "baseline" {
				values[7] = 1
			}
			if c.Context.Origin == "friend" {
				values[8] = 1
			}
			z := model.Intercept
			for i, value := range values {
				z += value * model.Coefficients[i]
			}
			require.InDelta(t, 1/(1+math.Exp(-z)), c.LR.Probability, 1e-12)
		}
		if mode == discovery.Recommendation {
			require.Positive(t, learned, "real Sort must invoke the loaded model")
		}
	}
}
