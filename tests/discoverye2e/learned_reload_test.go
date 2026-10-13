package discoverye2e

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"eigenflux_server/rpc/sort/discovery"
	"eigenflux_server/rpc/sort/discoverylr"
	"github.com/stretchr/testify/require"
)

// Exercise the running Sort watcher and Feed's frozen responses together. The
// deterministic fixture disables seen suppression to reuse one broadcast across
// model generations; normal discovery deduplication has separate E2E coverage.
func TestDiscoveryLRReloadWithoutServiceRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model.json")
	s := startStack(t, map[string]string{
		"DISCOVERY_LR_ENABLED": "true", "DISCOVERY_LR_MODEL_PATH": path,
		"DISCOVERY_LR_RELOAD_INTERVAL": "100ms", "DISABLE_DEDUP_IN_TEST": "true",
	})
	input := s.saved(t, "broadcast")
	request := discovery.Request{SourceKinds: []discovery.Kind{discovery.Broadcast}, NeedIDs: []string{fmt.Sprint(input.NeedInputID)}, Limit: 5}
	deliver := func(key string) (discovery.Response, discovery.Candidate) {
		t.Helper()
		response := s.recommend(t, request, key)
		require.Len(t, response.Items, 1)
		require.Equal(t, s.item, response.Items[0].Ref.ID)
		s.waitSamples(t, response.ImpressionID, 1)
		var raw string
		require.NoError(t, s.db.Table("replay_logs").Select("item_features").Where("impression_id=? AND agent_id=?", response.ImpressionID, s.owner).Scan(&raw).Error)
		candidate := decode[struct {
			Search discovery.Candidate `json:"search"`
		}](t, []byte(raw)).Search
		require.Equal(t, "rules", candidate.Score.ScorerType)
		return response, candidate
	}

	ruleResponse, ruleCandidate := deliver("reload-initial-rules")
	require.Nil(t, ruleCandidate.LR, "a missing initial bundle must keep serving with rules")
	errorCount := lrReloadCounter(t, "error")
	require.Positive(t, errorCount)

	writeLRBundle(t, path, "e2e-reload-v1", .2, true)
	require.Eventually(t, func() bool { return lrReloadCounter(t, "success") >= 1 }, 5*time.Second, 50*time.Millisecond)
	v1Response, v1 := deliver("reload-v1")
	require.NotNil(t, v1.LR)
	require.Equal(t, "e2e-reload-v1", v1.LR.ModelVersion)
	require.InDelta(t, .2, v1.LR.Probability, 1e-12)
	require.Equal(t, ruleResponse, s.recommend(t, request, "reload-initial-rules"), "recovering the model cannot rewrite a frozen response")
	s.waitSamples(t, ruleResponse.ImpressionID, 1)

	writeLRBundle(t, path, "e2e-reload-v2", .8, true)
	require.Eventually(t, func() bool { return lrReloadCounter(t, "success") >= 2 }, 5*time.Second, 50*time.Millisecond)
	_, v2 := deliver("reload-v2")
	require.NotNil(t, v2.LR)
	require.Equal(t, "e2e-reload-v2", v2.LR.ModelVersion)
	require.InDelta(t, .8, v2.LR.Probability, 1e-12)
	require.Equal(t, v1Response, s.recommend(t, request, "reload-v1"))
	_, frozenV1 := deliver("reload-v1")
	require.Equal(t, v1.LR, frozenV1.LR, "retry retains the original model evidence and one delivered sample")

	errorCount = lrReloadCounter(t, "error")
	writeLRBundle(t, path, "unpromoted-v3", .99, false)
	require.Eventually(t, func() bool { return lrReloadCounter(t, "error") > errorCount }, 5*time.Second, 50*time.Millisecond)
	_, retained := deliver("reload-invalid")
	require.Equal(t, v2.LR, retained.LR, "failed promotion must retain the last valid model")

	filtered := request
	filtered.Filters.Lang = []string{"zh"}
	require.Empty(t, s.recommend(t, filtered, "reload-hard-filter").Items, "a high probability cannot bypass language evidence")
	search := s.search(t, discovery.Request{Query: "landing page design", SourceKinds: []discovery.Kind{discovery.Broadcast}}, "reload-search")
	require.Len(t, search.Items, 1)
	s.waitSamples(t, search.ImpressionID, 1)
	var raw string
	require.NoError(t, s.db.Table("replay_logs").Select("item_features").Where("impression_id=?", search.ImpressionID).Scan(&raw).Error)
	searchCandidate := decode[struct {
		Search discovery.Candidate `json:"search"`
	}](t, []byte(raw)).Search
	require.Nil(t, searchCandidate.LR, "search must retain its rule scorer after hot reload")
}

func writeLRBundle(t *testing.T, path, version string, probability float64, promoted bool) {
	t.Helper()
	// Zero coefficients make the serving probability independently checkable;
	// the production loader still validates the full contract and self-tests.
	writeJSON(t, path+".tmp", map[string]any{
		"schema_version": 1, "model_type": "logistic_regression", "model_version": version,
		"feature_contract_version": discoverylr.Contract, "feature_names": discoverylr.FeatureNames,
		"coefficients": make([]float64, len(discoverylr.FeatureNames)), "intercept": math.Log(probability / (1 - probability)),
		"promotion_passed": promoted, "self_test_cases": []any{
			map[string]any{"features": make([]float64, len(discoverylr.FeatureNames)), "probability": probability},
			map[string]any{"features": []float64{1, 1, 1, 1, 1, 1, 1, 1, 1}, "probability": probability},
		},
	})
	require.NoError(t, os.Rename(path+".tmp", path))
}

func lrReloadCounter(t *testing.T, result string) float64 {
	t.Helper()
	port, err := strconv.Atoi(os.Getenv("SORT_RPC_PORT"))
	require.NoError(t, err)
	response, err := (&http.Client{Timeout: time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", port+1000))
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	prefix := `discovery_lr_reload_total{result="` + result + `"} `
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, prefix) {
			value, err := strconv.ParseFloat(strings.TrimPrefix(line, prefix), 64)
			require.NoError(t, err)
			return value
		}
	}
	return 0
}
