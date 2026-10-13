package consumer

import (
	"bytes"
	"context"
	"errors"
	"log"
	"testing"

	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPersistProcessedItemMarksFailedAndRetainsDeliveryOnPersistError(t *testing.T) {
	consumerPersistenceDB(t)
	originalUpdateProcessedItem := updateProcessedItem
	originalUpdateProcessedItemStatus := updateProcessedItemStatus
	defer func() {
		updateProcessedItem = originalUpdateProcessedItem
		updateProcessedItemStatus = originalUpdateProcessedItemStatus
	}()

	var statusItemID int64
	var statusValue int16

	updateProcessedItem = func(_ *gorm.DB, itemID int64, summary, broadcastType, domains string, keywords []string, expireTime, geo, sourceType, expectedResponse string, groupID int64, qualityScore float64, lang, timeliness, suggestion string, homepageEligible, homepageRealWorldRelevant bool, homepageRejectionReason, homepageEvaluationVersion string, status int16) error {
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
		"",
	)

	require.False(t, ok)
	assert.Equal(t, int64(123), statusItemID)
	assert.Equal(t, int16(2), statusValue)
	assert.Contains(t, logs.String(), "failed to persist processed item")
	assert.Contains(t, logs.String(), "itemID=123")
}

func TestPersistProcessedItemRetainsDeliveryWhenMarkFailedAlsoFails(t *testing.T) {
	consumerPersistenceDB(t)
	originalUpdateProcessedItem := updateProcessedItem
	originalUpdateProcessedItemStatus := updateProcessedItemStatus
	defer func() {
		updateProcessedItem = originalUpdateProcessedItem
		updateProcessedItemStatus = originalUpdateProcessedItemStatus
	}()

	updateProcessedItem = func(_ *gorm.DB, itemID int64, summary, broadcastType, domains string, keywords []string, expireTime, geo, sourceType, expectedResponse string, groupID int64, qualityScore float64, lang, timeliness, suggestion string, homepageEligible, homepageRealWorldRelevant bool, homepageRejectionReason, homepageEvaluationVersion string, status int16) error {
		return errors.New("persist failed")
	}
	updateProcessedItemStatus = func(_ *gorm.DB, itemID int64, status int16) error {
		return errors.New("status update failed")
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
		"",
	)

	require.False(t, ok)
	assert.Contains(t, logs.String(), "failed to persist processed item")
	assert.Contains(t, logs.String(), "failed to mark item as failed after persist error")
}

func TestHomepageCountryFromPrivateCard(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "canonical country", raw: `{"geo":"CN"}`, want: "CN"},
		{name: "trims country", raw: `{"geo":" SG "}`, want: "SG"},
		{name: "missing country", raw: `{}`, want: ""},
		{name: "invalid card", raw: `{`, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := homepageCountryFromPrivateCard(test.raw)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func consumerPersistenceDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := gdb.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	old := db.DB
	db.DB = gdb
	t.Cleanup(func() { db.DB = old; require.NoError(t, pool.Close()) })
	return gdb
}
