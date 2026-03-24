package consumer

import (
	"bytes"
	"context"
	"errors"
	"log"
	"testing"

	"eigenflux_server/pipeline/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPersistProcessedItemMarksFailedAndAcksOnPersistError(t *testing.T) {
	originalUpdateProcessedItem := updateProcessedItem
	originalUpdateProcessedItemStatus := updateProcessedItemStatus
	originalAckItemMessage := ackItemMessage
	defer func() {
		updateProcessedItem = originalUpdateProcessedItem
		updateProcessedItemStatus = originalUpdateProcessedItemStatus
		ackItemMessage = originalAckItemMessage
	}()

	var statusItemID int64
	var statusValue int16
	var acked bool

	updateProcessedItem = func(_ *gorm.DB, itemID int64, summary, broadcastType, domains string, keywords []string, expireTime, geo, sourceType, expectedResponse string, groupID int64, qualityScore float64, lang, timeliness string, status int16) error {
		assert.Equal(t, int64(123), itemID)
		assert.Equal(t, int16(3), status)
		assert.Equal(t, "info", broadcastType)
		return errors.New("persist failed")
	}
	updateProcessedItemStatus = func(_ *gorm.DB, itemID int64, status int16) error {
		statusItemID = itemID
		statusValue = status
		return nil
	}
	ackItemMessage = func(_ context.Context, stream, group, msgID string) error {
		assert.Equal(t, itemStream, stream)
		assert.Equal(t, itemGroup, group)
		assert.Equal(t, "1-0", msgID)
		acked = true
		return nil
	}

	var logs bytes.Buffer
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	defer func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
	}()
	log.SetOutput(&logs)
	log.SetFlags(0)

	ok := persistProcessedItem(
		context.Background(),
		"1-0",
		123,
		&llm.ExtractResult{
			Summary:       "summary",
			BroadcastType: "info",
			Domains:       []string{"infra"},
			Keywords:      []string{"redis"},
			ExpireTime:    "2026-03-25T00:00:00Z",
			Geo:           "CN",
			SourceType:    "original",
			Quality:       0.92,
			Lang:          "en",
			Timeliness:    "timely",
		},
		"infra",
		"reply",
		456,
	)

	require.False(t, ok)
	assert.True(t, acked)
	assert.Equal(t, int64(123), statusItemID)
	assert.Equal(t, int16(2), statusValue)
	assert.Contains(t, logs.String(), "failed to persist processed item 123")
	assert.Contains(t, logs.String(), "broadcast_type=info")
}

func TestPersistProcessedItemStillAcksWhenMarkFailedAlsoFails(t *testing.T) {
	originalUpdateProcessedItem := updateProcessedItem
	originalUpdateProcessedItemStatus := updateProcessedItemStatus
	originalAckItemMessage := ackItemMessage
	defer func() {
		updateProcessedItem = originalUpdateProcessedItem
		updateProcessedItemStatus = originalUpdateProcessedItemStatus
		ackItemMessage = originalAckItemMessage
	}()

	var acked bool

	updateProcessedItem = func(_ *gorm.DB, itemID int64, summary, broadcastType, domains string, keywords []string, expireTime, geo, sourceType, expectedResponse string, groupID int64, qualityScore float64, lang, timeliness string, status int16) error {
		return errors.New("persist failed")
	}
	updateProcessedItemStatus = func(_ *gorm.DB, itemID int64, status int16) error {
		return errors.New("status update failed")
	}
	ackItemMessage = func(_ context.Context, stream, group, msgID string) error {
		acked = true
		return nil
	}

	var logs bytes.Buffer
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	defer func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
	}()
	log.SetOutput(&logs)
	log.SetFlags(0)

	ok := persistProcessedItem(
		context.Background(),
		"2-0",
		789,
		&llm.ExtractResult{
			BroadcastType: "alert",
		},
		"",
		"",
		789,
	)

	require.False(t, ok)
	assert.True(t, acked)
	assert.Contains(t, logs.String(), "failed to persist processed item 789")
	assert.Contains(t, logs.String(), "failed to mark item 789 as failed after persist error")
}
