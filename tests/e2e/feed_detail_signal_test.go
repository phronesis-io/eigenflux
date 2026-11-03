package e2e_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/followuplog"
	"eigenflux_server/pkg/recall"
	"eigenflux_server/tests/testutil"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestFeedDetailSignals(t *testing.T) {
	token, agent, item, own := feedbackMetadataFixture(t)
	impression := fmt.Sprintf("detail-%d", item)
	for _, path := range []string{
		fmt.Sprintf("/api/v1/items/%d?impression_id=%s", item, impression),
		fmt.Sprintf("/api/v1/items/%d?impression_id=%s", item, impression),
		fmt.Sprintf("/api/v1/items/%d", item),
		fmt.Sprintf("/api/v1/items/%d?impression_id=%s", own, impression),
	} {
		body, status, err := feedbackMetadataRequest("GET", path, token, nil)
		require.NoError(t, err)
		require.Equal(t, 200, status, body)
	}
	body, status, err := feedbackMetadataRequest("GET", fmt.Sprintf("/api/v1/items/%d", item+2), token, nil)
	require.NoError(t, err)
	require.Equal(t, 404, status, body)
	body, status, err = feedbackMetadataRequest("GET", fmt.Sprintf("/api/v1/items/%d", item), "", nil)
	require.NoError(t, err)
	require.Equal(t, 401, status, body)
	require.Eventually(t, func() bool {
		var n int
		return testutil.TestDB.QueryRow(`SELECT count(*) FROM followup_labels WHERE agent_id=$1 AND item_id=$2 AND kind='detail_fetch'`, agent, item).Scan(&n) == nil && n == 2
	}, 10*time.Second, 20*time.Millisecond)
	waitForStreamDrained(t, followuplog.StreamName, followuplog.GroupName)
	var attributed, unknown, authorReads int
	require.NoError(t, testutil.TestDB.QueryRow(`SELECT count(*) FILTER (WHERE impression_id=$3), count(*) FILTER (WHERE impression_id=''), count(*) FILTER (WHERE item_id=$4) FROM followup_labels WHERE agent_id=$1 AND item_id IN ($2,$4) AND kind='detail_fetch'`, agent, item, impression, own).Scan(&attributed, &unknown, &authorReads))
	require.Equal(t, 1, attributed)
	require.Equal(t, 1, unknown)
	require.Zero(t, authorReads)
	rdb := testutil.GetTestRedis()
	require.ErrorIs(t, rdb.ZScore(context.Background(), recall.SurfaceHistoryKey(config.Load().RecallRedisNamespace, agent), fmt.Sprint(item)).Err(), redis.Nil)
	var scores int
	require.NoError(t, testutil.TestDB.QueryRow(`SELECT count(*) FROM feedback_logs WHERE agent_id=$1 AND item_id=$2`, agent, item).Scan(&scores))
	require.Zero(t, scores)
	// Clients cannot mint the reserved server-observed event kind.
	resp := testutil.DoPost(t, "/api/v1/items/events", map[string]any{"events": []map[string]any{{"item_id": fmt.Sprint(item), "kind": followuplog.DetailFetch, "dedup_key": "client-detail"}}}, token)
	require.EqualValues(t, 0, resp["data"].(map[string]any)["accepted"])
	require.EqualValues(t, 1, resp["data"].(map[string]any)["skipped"])
}
