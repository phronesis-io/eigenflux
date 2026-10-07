package discoverye2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"eigenflux_server/pkg/activity"
	"eigenflux_server/pkg/cache/keys"
	"eigenflux_server/pkg/mq"
	"github.com/stretchr/testify/require"
)

// One successful HTTP Feed pull must produce exactly one feed_pull activity
// carrying the final item count, and a concurrent pull must surface as 409
// without releasing the lock held by the in-flight request.
func TestFeedHTTPPullActivityAndConflict(t *testing.T) {
	s := startStack(t)
	s.changeInputs(t, `UPDATE agent_context_revisions SET compiled_context='{"intent_actions":[]}'::jsonb WHERE agent_id=?`, s.other)
	for range 2 {
		feed := decode[map[string]any](t, s.call(t, "POST", "/api/v2/feed", s.otherToken, "", map[string]any{"limit": 5}, 200))
		require.Equal(t, []any{}, feed["items"])
	}
	lockKey := fmt.Sprintf(keys.DeliveryPage, s.other) + keys.LockSuffix
	locked, err := mq.RDB.SetNX(context.Background(), lockKey, "feed-e2e-held-lock", time.Minute).Result()
	require.NoError(t, err)
	require.True(t, locked)
	s.call(t, "POST", "/api/v2/feed", s.otherToken, "", map[string]any{}, 409)
	token, err := mq.RDB.Get(context.Background(), lockKey).Result()
	require.NoError(t, err)
	require.Equal(t, "feed-e2e-held-lock", token, "conflicting request cannot release another request's lock")
	require.NoError(t, mq.RDB.Del(context.Background(), lockKey).Err())

	activityCount := func() int {
		rows, err := mq.RDB.XRange(context.Background(), activity.StreamName, "-", "+").Result()
		require.NoError(t, err)
		count := 0
		for _, row := range rows {
			if row.Values["agent_id"] == strconv.FormatInt(s.other, 10) && row.Values["event_type"] == "feed_pull" {
				var detail struct {
					Count int `json:"count"`
				}
				require.NoError(t, json.Unmarshal([]byte(row.Values["detail"].(string)), &detail))
				require.Zero(t, detail.Count)
				count++
			}
		}
		return count
	}
	require.Eventually(t, func() bool { return activityCount() >= 2 }, 4*time.Second, 20*time.Millisecond)
	// The publisher has a three-second deadline. Wait across it to detect an
	// additional asynchronous publish, rather than passing on the first two rows.
	require.Never(t, func() bool { return activityCount() > 2 }, 3500*time.Millisecond, 20*time.Millisecond)
	require.Equal(t, 2, activityCount(), "one successful HTTP Feed pull must produce one activity event")
}
