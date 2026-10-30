package consumer

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/itemstats"
	"eigenflux_server/pkg/mq"
	itemdal "eigenflux_server/rpc/item/dal"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupItemStatsConsumerRedis(t *testing.T) {
	t.Helper()

	mr, err := miniredis.Run()
	require.NoError(t, err)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
		mr.Close()
	})

	mq.RDB = client
	t.Cleanup(func() {
		mq.RDB = nil
	})
}

func TestItemStatsConsumerRetriesPendingMessageUntilSuccess(t *testing.T) {
	setupItemStatsConsumerRedis(t)

	cfg := &config.Config{FeedbackConsumerWorkers: 1}
	consumer := NewItemStatsConsumer(cfg, nil)
	consumer.consumerName = "test-item-stats-success"
	consumer.readBlock = 10 * time.Millisecond
	consumer.retryMinIdle = 150 * time.Millisecond
	consumer.maxRetries = 3

	var attempts atomic.Int64
	consumer.handleEvent = func(ctx context.Context, msgID string, event itemstats.Event) error {
		if attempts.Add(1) < 3 {
			return errors.New("transient failure")
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		consumer.Start(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	_, err := itemstats.PublishConsumed(context.Background(), 101, 202)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		pending, pendingErr := mq.PendingCount(context.Background(), itemstats.StreamName, itemstats.GroupName)
		return pendingErr == nil && attempts.Load() == 3 && pending == 0
	}, 2*time.Second, 20*time.Millisecond)

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int64(3), attempts.Load())
}

func TestItemStatsConsumerDropsMessageAfterMaxRetries(t *testing.T) {
	setupItemStatsConsumerRedis(t)

	cfg := &config.Config{FeedbackConsumerWorkers: 1}
	consumer := NewItemStatsConsumer(cfg, nil)
	consumer.consumerName = "test-item-stats-drop"
	consumer.readBlock = 10 * time.Millisecond
	consumer.retryMinIdle = 150 * time.Millisecond
	consumer.maxRetries = 3

	var attempts atomic.Int64
	consumer.handleEvent = func(ctx context.Context, msgID string, event itemstats.Event) error {
		attempts.Add(1)
		return errors.New("persistent failure")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		consumer.Start(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	_, err := itemstats.PublishConsumed(context.Background(), 303, 404)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		pending, pendingErr := mq.PendingCount(context.Background(), itemstats.StreamName, itemstats.GroupName)
		return pendingErr == nil && attempts.Load() == 3 && pending == 0
	}, 2*time.Second, 20*time.Millisecond)

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int64(3), attempts.Load())
}

func TestItemStatsConsumerAcksUnknownItemWithoutRetry(t *testing.T) {
	setupItemStatsConsumerRedis(t)

	consumer := NewItemStatsConsumer(&config.Config{FeedbackConsumerWorkers: 1}, nil)
	consumer.consumerName = "test-item-stats-unknown"
	consumer.readBlock = 10 * time.Millisecond
	consumer.retryMinIdle = 50 * time.Millisecond

	var attempts atomic.Int64
	consumer.handleEvent = func(ctx context.Context, msgID string, event itemstats.Event) error {
		attempts.Add(1)
		return fmt.Errorf("lookup item stats: %w", gorm.ErrRecordNotFound)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		consumer.Start(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	_, err := itemstats.PublishFeedback(context.Background(), itemstats.Feedback{AgentID: 303, ItemID: 373718292642136060, Score: 1})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		pending, pendingErr := mq.PendingCount(context.Background(), itemstats.StreamName, itemstats.GroupName)
		return pendingErr == nil && attempts.Load() == 1 && pending == 0
	}, 2*time.Second, 20*time.Millisecond)

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int64(1), attempts.Load(), "a missing item must not be retried")
}

func TestPersistFeedbackEventDeduplicatesByStreamMessageID(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, gdb.AutoMigrate(&itemdal.ItemStats{}, &FeedbackLog{}))

	prevDB := db.DB
	db.DB = gdb
	t.Cleanup(func() {
		db.DB = prevDB
	})

	now := time.Now().UnixMilli()
	require.NoError(t, gdb.Create(&itemdal.ItemStats{
		ItemID:        101,
		AuthorAgentID: 7,
		CreatedAt:     now,
		UpdatedAt:     now,
	}).Error)

	consumer := NewItemStatsConsumer(&config.Config{FeedbackConsumerWorkers: 1}, nil)
	event := itemstats.Event{
		EventType:    itemstats.EventTypeFeedback,
		AgentID:      7,
		ItemID:       101,
		Score:        2,
		ImpressionID: "imp_123",
	}

	inserted, err := consumer.persistFeedbackEvent("1744633934954-0", event)
	require.NoError(t, err)
	require.True(t, inserted)

	inserted, err = consumer.persistFeedbackEvent("1744633934954-0", event)
	require.NoError(t, err)
	require.False(t, inserted)

	var stats itemdal.ItemStats
	require.NoError(t, gdb.First(&stats, "item_id = ?", 101).Error)
	assert.Equal(t, int64(1), stats.Score2Count)
	assert.Equal(t, int64(2), stats.TotalScore)

	var logs []FeedbackLog
	require.NoError(t, gdb.Order("id ASC").Find(&logs).Error)
	require.Len(t, logs, 1)
	assert.Equal(t, "1744633934954-0", logs[0].StreamMessageID)
	assert.Equal(t, "imp_123", logs[0].ImpressionID)
	assert.Equal(t, int64(7), logs[0].AgentID)
	assert.Equal(t, int64(101), logs[0].ItemID)
	assert.Equal(t, int16(2), logs[0].Score)
}

func TestPersistFeedbackEventDeduplicatesClientKey(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&itemdal.ItemStats{}, &FeedbackLog{}))
	require.NoError(t, database.Exec(`CREATE UNIQUE INDEX uq_feedback_logs_agent_client_key ON feedback_logs(agent_id,client_key) WHERE client_key<>''`).Error)
	previous := db.DB
	db.DB = database
	t.Cleanup(func() { db.DB = previous })
	require.NoError(t, database.Create(&itemdal.ItemStats{ItemID: 202, AuthorAgentID: 9}).Error)
	c := NewItemStatsConsumer(&config.Config{FeedbackConsumerWorkers: 1}, nil)
	no := false
	event := itemstats.Event{EventType: itemstats.EventTypeFeedback, AgentID: 7, ItemID: 202, Score: 2, ClientKey: "key", ImpressionID: "imp", ImpressionSource: "explicit", ContractVersion: "v2", Surfaced: &no}
	inserted, err := c.persistFeedbackEvent("keyed-1", event)
	require.NoError(t, err)
	require.True(t, inserted)
	inserted, err = c.persistFeedbackEvent("keyed-2", event)
	require.NoError(t, err)
	require.False(t, inserted)
	var rows []FeedbackLog
	require.NoError(t, database.Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, &no, rows[0].Surfaced)
	require.Equal(t, "explicit", rows[0].ImpressionSource)
	require.Equal(t, "v2", rows[0].ContractVersion)
	var counts itemdal.ItemStats
	require.NoError(t, database.First(&counts, "item_id = ?", 202).Error)
	require.Equal(t, int64(1), counts.Score2Count)
	// Keys belong to an Agent. An empty key retains event-by-event counting.
	event.AgentID = 8
	inserted, err = c.persistFeedbackEvent("other-agent", event)
	require.NoError(t, err)
	require.True(t, inserted)
	event.ClientKey = ""
	for _, id := range []string{"legacy-1", "legacy-2"} {
		inserted, err = c.persistFeedbackEvent(id, event)
		require.NoError(t, err)
		require.True(t, inserted)
	}
	require.NoError(t, database.First(&counts, "item_id = ?", 202).Error)
	require.Equal(t, int64(4), counts.Score2Count)
}
