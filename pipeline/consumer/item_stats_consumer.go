package consumer

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/itemstats"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/milestone"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/stats"
	itemdal "eigenflux_server/rpc/item/dal"
)

const (
	itemStatsConsumerName      = "item-stats-worker-1"
	itemStatsBatchSize         = int64(10)
	itemStatsMaxRetryCount     = int64(3)
	itemStatsRetryMinIdle      = time.Second
	itemStatsRetryPollInterval = 200 * time.Millisecond
	itemStatsReadBlock         = 500 * time.Millisecond
)

type ItemStatsConsumer struct {
	maxWorkers   int
	milestoneSvc *milestone.Service
	consumerName string
	maxRetries   int64
	retryMinIdle time.Duration
	readBlock    time.Duration
	handleEvent  func(context.Context, itemstats.Event) error
}

func NewItemStatsConsumer(cfg *config.Config, milestoneSvc *milestone.Service) *ItemStatsConsumer {
	c := &ItemStatsConsumer{
		maxWorkers:   cfg.FeedbackConsumerWorkers,
		milestoneSvc: milestoneSvc,
		consumerName: itemStatsConsumerName,
		maxRetries:   itemStatsMaxRetryCount,
		retryMinIdle: itemStatsRetryMinIdle,
		readBlock:    itemStatsReadBlock,
	}
	c.handleEvent = c.handleEventDefault
	return c
}

func (c *ItemStatsConsumer) Start(ctx context.Context) {
	logger.Default().Info("ItemStatsConsumer starting", "workers", c.maxWorkers)

	if err := mq.EnsureConsumerGroup(ctx, itemstats.StreamName, itemstats.GroupName); err != nil {
		logger.Default().Error("ItemStatsConsumer failed to create consumer group", "err", err)
		os.Exit(1)
	}

	type msgTask struct {
		id     string
		values map[string]interface{}
	}
	msgChan := make(chan msgTask, c.maxWorkers*2)
	var wg sync.WaitGroup

	for i := 0; i < c.maxWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			logger.Default().Info("ItemStatsConsumer worker started", "workerID", workerID)
			for task := range msgChan {
				c.processMessage(ctx, task.id, task.values)
			}
			logger.Default().Info("ItemStatsConsumer worker stopped", "workerID", workerID)
		}(i)
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				logger.Default().Info("ItemStatsConsumer context cancelled, closing message channel")
				close(msgChan)
				return
			default:
			}

			msgs, err := c.nextBatch(ctx)
			if err != nil {
				logger.Default().Error("ItemStatsConsumer consume error", "err", err)
				time.Sleep(time.Second)
				continue
			}

			for _, msg := range msgs {
				task := msgTask{id: msg.Message.ID, values: msg.Message.Values}
				select {
				case msgChan <- task:
				case <-ctx.Done():
					logger.Default().Info("ItemStatsConsumer context cancelled while sending message")
					close(msgChan)
					return
				}
			}
		}
	}()

	<-ctx.Done()
	logger.Default().Info("ItemStatsConsumer shutting down, waiting for workers to finish...")
	wg.Wait()
	logger.Default().Info("ItemStatsConsumer all workers stopped")
}

func (c *ItemStatsConsumer) nextBatch(ctx context.Context) ([]mq.PendingMessage, error) {
	reclaimed, err := mq.ConsumePending(ctx, itemstats.StreamName, itemstats.GroupName, c.consumerName, itemStatsBatchSize, c.retryMinIdle)
	if err != nil {
		return nil, err
	}
	if len(reclaimed) > 0 {
		msgs := make([]mq.PendingMessage, 0, len(reclaimed))
		for _, pending := range reclaimed {
			if pending.RetryCount >= c.maxRetries {
				logger.Default().Warn("ItemStatsConsumer dropping message after failed attempts", "msgID", pending.Message.ID, "retryCount", pending.RetryCount, "lastConsumer", pending.Consumer)
				c.ackMessage(ctx, pending.Message.ID)
				continue
			}
			msgs = append(msgs, pending)
		}
		if len(msgs) > 0 {
			return msgs, nil
		}
	}

	pendingCount, err := mq.PendingCount(ctx, itemstats.StreamName, itemstats.GroupName)
	if err != nil {
		return nil, err
	}
	if pendingCount > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(itemStatsRetryPollInterval):
			return nil, nil
		}
	}

	messages, err := mq.ConsumeWithBlock(ctx, itemstats.StreamName, itemstats.GroupName, c.consumerName, itemStatsBatchSize, c.readBlock)
	if err != nil {
		return nil, err
	}

	msgs := make([]mq.PendingMessage, 0, len(messages))
	for _, message := range messages {
		msgs = append(msgs, mq.PendingMessage{Message: message})
	}
	return msgs, nil
}

func (c *ItemStatsConsumer) processMessage(ctx context.Context, msgID string, values map[string]interface{}) {
	event, err := itemstats.ParseEvent(values)
	if err != nil {
		logger.Default().Warn("ItemStatsConsumer invalid message", "err", err)
		c.ackMessage(ctx, msgID)
		return
	}

	if err := c.handleEvent(ctx, event); err != nil {
		logger.Default().Error("ItemStatsConsumer failed to process event", "eventType", event.EventType, "itemID", event.ItemID, "err", err)
		return
	}

	logger.Default().Info("ItemStatsConsumer processed event", "eventType", event.EventType, "itemID", event.ItemID)
	c.ackMessage(ctx, msgID)
}

func (c *ItemStatsConsumer) handleEventDefault(ctx context.Context, event itemstats.Event) error {
	switch event.EventType {
	case itemstats.EventTypeConsumed:
		logger.Default().Debug("ItemStatsConsumer processing consumed event", "agentID", event.AgentID, "itemID", event.ItemID)
		if err := itemdal.IncrementConsumedCount(db.DB, event.ItemID); err != nil {
			return err
		}
		return c.checkMilestone(ctx, event.ItemID, milestone.MetricConsumed, func(stats *itemdal.ItemStats) int64 {
			return stats.ConsumedCount
		})
	case itemstats.EventTypeFeedback:
		logger.Default().Debug("ItemStatsConsumer processing feedback event", "agentID", event.AgentID, "itemID", event.ItemID, "score", event.Score, "impressionID", event.ImpressionID)
		if err := itemdal.IncrementItemScore(db.DB, event.ItemID, event.Score); err != nil {
			return err
		}

		// Increment high-quality count for positive feedback (score 1 or 2)
		if event.Score == 1 || event.Score == 2 {
			go func() {
				bgCtx := context.Background()
				if err := stats.IncrHighQualityCount(bgCtx, mq.RDB); err != nil {
					logger.Default().Warn("ItemStatsConsumer failed to increment high quality count", "err", err)
				}
			}()
		}

		switch event.Score {
		case 1:
			return c.checkMilestone(ctx, event.ItemID, milestone.MetricScore1, func(stats *itemdal.ItemStats) int64 {
				return stats.Score1Count
			})
		case 2:
			return c.checkMilestone(ctx, event.ItemID, milestone.MetricScore2, func(stats *itemdal.ItemStats) int64 {
				return stats.Score2Count
			})
		default:
			return nil
		}
	default:
		return fmt.Errorf("unsupported event type %q", event.EventType)
	}
}

func (c *ItemStatsConsumer) ackMessage(ctx context.Context, msgID string) {
	if err := mq.Ack(ctx, itemstats.StreamName, itemstats.GroupName, msgID); err != nil {
		logger.Default().Warn("ItemStatsConsumer failed to ack message", "msgID", msgID, "err", err)
	}
}

func (c *ItemStatsConsumer) checkMilestone(ctx context.Context, itemID int64, metricKey string, currentCount func(*itemdal.ItemStats) int64) error {
	if c.milestoneSvc == nil {
		return nil
	}

	stats, err := itemdal.GetItemStatsByID(db.DB, itemID)
	if err != nil {
		return err
	}
	return c.milestoneSvc.Check(ctx, itemID, metricKey, currentCount(stats))
}
