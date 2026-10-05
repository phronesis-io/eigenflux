package discoverye2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"eigenflux_server/pkg/activity"
	"eigenflux_server/pkg/cache/keys"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/rpc/sort/discovery"
	"github.com/stretchr/testify/require"
)

func TestRecommendationAAHTTP(t *testing.T) {
	s := startStack(t)
	baselineLog, err := os.ReadFile(filepath.Join(s.logs, "api.log"))
	require.NoError(t, err)
	s.changeInputs(t, `UPDATE agent_context_revisions SET compiled_context='{"intent_actions":[]}'::jsonb WHERE agent_id=?`, s.other)
	for range 2 {
		feed := decode[map[string]any](t, s.call(t, "POST", "/api/v2/feed", s.otherToken, "", map[string]any{"limit": 5}, 200))
		require.Equal(t, []any{}, feed["items"])
	}
	s.call(t, "POST", "/api/v2/feed", s.otherToken, "", map[string]any{"unknown_field": true}, 400)
	s.call(t, "POST", "/api/v2/feed", "invalid-token", "", map[string]any{}, 401)
	lockKey := fmt.Sprintf(keys.DeliveryPage, s.other) + keys.LockSuffix
	locked, err := mq.RDB.SetNX(context.Background(), lockKey, "aa-e2e-held-lock", time.Minute).Result()
	require.NoError(t, err)
	require.True(t, locked)
	s.call(t, "POST", "/api/v2/feed", s.otherToken, "", map[string]any{}, 409)
	token, err := mq.RDB.Get(context.Background(), lockKey).Result()
	require.NoError(t, err)
	require.Equal(t, "aa-e2e-held-lock", token, "conflicting request cannot release another request's lock")
	require.NoError(t, mq.RDB.Del(context.Background(), lockKey).Err())
	s.sql(t, `UPDATE agent_credential_sessions SET scopes=ARRAY[]::text[] WHERE principal_id IN (SELECT principal_id FROM agent_principals WHERE agent_id=?)`, s.other)
	s.call(t, "POST", "/api/v2/feed", s.otherToken, "", map[string]any{}, 403)
	input := s.saved(t, "broadcast")
	request := discovery.Request{NeedIDs: []string{fmt.Sprint(input.NeedInputID)}, SourceKinds: []discovery.Kind{discovery.Broadcast}}
	nonempty := s.recommend(t, request, "aa-frozen")
	require.Len(t, nonempty.Items, 1)
	require.Equal(t, nonempty, s.recommend(t, request, "aa-frozen"))
	s.waitSamples(t, nonempty.ImpressionID, 1)

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
	raw, err := os.ReadFile(filepath.Join(s.logs, "api.log"))
	require.NoError(t, err)
	type observation struct {
		AgentID      string `json:"agent_id"`
		Arm          string `json:"arm"`
		Outcome      string `json:"outcome"`
		ItemCount    *int   `json:"item_count"`
		Pipeline     string `json:"pipeline_version"`
		ID           string `json:"observation_id"`
		ImpressionID string `json:"impression_id"`
		ErrorCode    string `json:"error_code"`
	}
	var observations []observation
	var deliveries []observation
	require.GreaterOrEqual(t, len(raw), len(baselineLog))
	for _, line := range strings.Split(string(raw[len(baselineLog):]), "\n") {
		var entry struct {
			Msg         string          `json:"msg"`
			Observation json.RawMessage `json:"observation"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil || entry.Msg != "recommendation_aa_observation" {
			continue
		}
		var o observation
		require.NoError(t, json.Unmarshal(entry.Observation, &o))
		if o.AgentID == strconv.FormatInt(s.other, 10) {
			observations = append(observations, o)
		}
		if o.AgentID == strconv.FormatInt(s.owner, 10) {
			deliveries = append(deliveries, o)
		}
	}
	require.Len(t, observations, 5, "authenticated empty/error completions only; bad bearer cannot enroll")
	require.Equal(t, observations[0].Arm, observations[1].Arm)
	require.Equal(t, observations[0].Arm, observations[2].Arm)
	require.NotEqual(t, observations[0].ID, observations[1].ID)
	for _, o := range observations[:2] {
		require.Equal(t, "empty", o.Outcome)
		require.NotNil(t, o.ItemCount)
		require.Zero(t, *o.ItemCount)
		require.Equal(t, "need_search_v1", o.Pipeline)
	}
	for i, o := range observations[2:] {
		if i == 1 {
			require.Equal(t, "in_progress", o.Outcome)
			require.Equal(t, "FEED_REQUEST_IN_PROGRESS", o.ErrorCode)
		} else {
			require.Equal(t, "http_error", o.Outcome)
		}
		require.Nil(t, o.ItemCount)
		require.Equal(t, observations[0].Arm, o.Arm)
	}
	require.Len(t, deliveries, 2)
	for _, o := range deliveries {
		require.Equal(t, "nonempty", o.Outcome)
		require.Equal(t, nonempty.ImpressionID, o.ImpressionID)
		require.NotNil(t, o.ItemCount)
		require.Equal(t, 1, *o.ItemCount)
		require.Equal(t, "need_search_v1", o.Pipeline)
	}
	require.Equal(t, deliveries[0].Arm, deliveries[1].Arm)
	require.NotEqual(t, deliveries[0].ID, deliveries[1].ID)
}
