package itemstats

import (
	"context"
	"eigenflux_server/pkg/mq"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEventConsumed(t *testing.T) {
	event, err := ParseEvent(map[string]interface{}{
		"event_type": EventTypeConsumed,
		"agent_id":   "101",
		"item_id":    "202",
	})
	require.NoError(t, err)
	assert.Equal(t, EventTypeConsumed, event.EventType)
	assert.Equal(t, int64(101), event.AgentID)
	assert.Equal(t, int64(202), event.ItemID)
	assert.Equal(t, 0, event.Score)
}

func TestParseEventFeedback(t *testing.T) {
	event, err := ParseEvent(map[string]interface{}{
		"event_type": EventTypeFeedback,
		"agent_id":   "101",
		"item_id":    "202",
		"score":      "2",
	})
	require.NoError(t, err)
	assert.Equal(t, EventTypeFeedback, event.EventType)
	assert.Equal(t, int64(101), event.AgentID)
	assert.Equal(t, int64(202), event.ItemID)
	assert.Equal(t, 2, event.Score)
	assert.Empty(t, event.ImpressionID)
}

func TestParseEventFeedbackWithImpressionID(t *testing.T) {
	event, err := ParseEvent(map[string]interface{}{
		"event_type":    EventTypeFeedback,
		"agent_id":      "101",
		"item_id":       "202",
		"score":         "1",
		"impression_id": "imp_12345",
	})
	require.NoError(t, err)
	assert.Equal(t, EventTypeFeedback, event.EventType)
	assert.Equal(t, int64(101), event.AgentID)
	assert.Equal(t, int64(202), event.ItemID)
	assert.Equal(t, 1, event.Score)
	assert.Equal(t, "imp_12345", event.ImpressionID)
}

func TestParseEventRejectsInvalidPayload(t *testing.T) {
	_, err := ParseEvent(map[string]interface{}{
		"event_type": EventTypeFeedback,
		"agent_id":   "101",
		"item_id":    "202",
		"score":      "9",
	})
	require.Error(t, err)
}

func TestFeedbackMetadataRoundTrip(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	previous := mq.RDB
	mq.RDB = rdb
	t.Cleanup(func() { mq.RDB = previous; rdb.Close() })
	no := false
	for _, feedback := range []Feedback{{AgentID: 1, ItemID: 2, Score: 0}, {AgentID: 1, ItemID: 2, Score: 2, ImpressionID: "imp", ClientKey: "key", ImpressionSource: "cli_inferred", ContractVersion: "feedback/v2", Surfaced: &no}} {
		id, err := PublishFeedback(context.Background(), feedback)
		require.NoError(t, err)
		messages, err := rdb.XRange(context.Background(), StreamName, id, id).Result()
		require.NoError(t, err)
		require.Len(t, messages, 1)
		event, err := ParseEvent(messages[0].Values)
		require.NoError(t, err)
		assert.Equal(t, feedback.AgentID, event.AgentID)
		assert.Equal(t, feedback.ItemID, event.ItemID)
		assert.Equal(t, feedback.Score, event.Score)
		assert.Equal(t, feedback.ClientKey, event.ClientKey)
		assert.Equal(t, feedback.ImpressionSource, event.ImpressionSource)
		assert.Equal(t, feedback.ContractVersion, event.ContractVersion)
		assert.Equal(t, feedback.Surfaced, event.Surfaced)
		if feedback.Surfaced == nil {
			require.NotContains(t, messages[0].Values, "surfaced")
			require.NotContains(t, messages[0].Values, "client_key")
		} else {
			require.Equal(t, "false", messages[0].Values["surfaced"])
		}
	}
	_, err := ParseEvent(map[string]interface{}{"event_type": "feedback", "agent_id": "1", "item_id": "2", "score": "0", "surfaced": "invalid"})
	require.Error(t, err)
}
