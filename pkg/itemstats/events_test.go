package itemstats

import (
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
