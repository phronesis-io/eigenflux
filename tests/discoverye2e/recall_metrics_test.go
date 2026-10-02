package discoverye2e

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"eigenflux_server/rpc/sort/discovery"
	"github.com/stretchr/testify/require"
)

func recallCounter(t *testing.T, portEnv, metric string, sources ...string) float64 {
	t.Helper()
	port, err := strconv.Atoi(os.Getenv(portEnv))
	require.NoError(t, err)
	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", port+1000))
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	source := "keyword"
	if len(sources) > 0 {
		source = sources[0]
	}
	prefix := metric + `{source="` + source + `"} `
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, prefix) {
			count, err := strconv.ParseFloat(strings.TrimPrefix(line, prefix), 64)
			require.NoError(t, err)
			return count
		}
	}
	return 0
}

func TestDiscoveryRecallMetrics(t *testing.T) {
	s := startStack(t)
	input := s.saved(t, "broadcast")
	request := discovery.Request{NeedIDs: []string{fmt.Sprint(input.NeedInputID)}, SourceKinds: []discovery.Kind{discovery.Broadcast}}
	candidates := func() float64 { return recallCounter(t, "SORT_RPC_PORT", "recall_feed_total") }
	impressions := func() float64 { return recallCounter(t, "FEED_RPC_PORT", "recall_impression_total") }
	beforeCandidates, beforeImpressions := candidates(), impressions()
	result := s.recommend(t, request, "recall-metrics")
	require.Len(t, result.Items, 1)
	require.Equal(t, s.item, result.Items[0].Ref.ID)
	require.Equal(t, beforeCandidates+1, candidates())
	require.Equal(t, beforeImpressions+1, impressions())
	require.Equal(t, result, s.recommend(t, request, "recall-metrics"))
	require.Equal(t, beforeCandidates+1, candidates(), "retry must not rerun recall")
	require.Equal(t, beforeImpressions+1, impressions(), "retry must not count another delivery")
	s.search(t, discovery.Request{Query: "landing page design", SourceKinds: []discovery.Kind{discovery.Broadcast}}, "")
	require.Equal(t, beforeCandidates+1, candidates(), "search must not enter feed metrics")
	require.Equal(t, beforeImpressions+1, impressions(), "search must not enter feed metrics")
}
