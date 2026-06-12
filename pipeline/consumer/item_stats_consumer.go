package consumer

import (
	"context"
	"fmt"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/itemstats"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/milestone"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/stats"
	itemdal "eigenflux_server/rpc/item/dal"
	"gorm.io/gorm"
)

const (
	itemStatsConsumerName      = "item-stats-worker-1"
	itemStatsBatchSize         = int64(10)
	itemStatsMaxRetryCount     = int64(3)
	itemStatsRetryMinIdle      = time.Second
	itemStatsRetryPollInterval = 200 * time.Millisecond
	itemStatsReadBlock         = 500 * time.Millisecond
)

// ItemStatsConsumer uses retry-aware mode: failures keep the message pending
// so it can be reclaimed on the next pass, up to maxRetries times.
//
// The tunable fields (consumerName, readBlock, retryMinIdle, maxRetries,
// handleEvent) are kept on the consumer struct so existing miniredis-based
// tests can override them between NewItemStatsConsumer and Start.
type ItemStatsConsumer struct {
	maxWorkers   int
	milestoneSvc *milestone.Service
	consumerName string
	maxRetries   int64
	retryMinIdle time.Duration
	readBlock    time.Duration
	handleEvent  func(context.Context, string, itemstats.Event) error
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
	runner := &StreamConsumer{
		Name:                    "ItemStatsConsumer",
		Stream:                  itemstats.StreamName,
		Group:                   itemstats.GroupName,
		ConsumerName:            c.consumerName,
		MetricsLabel:            "item:stats",
		Workers:                 c.maxWorkers,
		BatchSize:               itemStatsBatchSize,
		MaxRetries:              c.maxRetries,
		RetryMinIdle:            c.retryMinIdle,
		PollInterval:            itemStatsRetryPollInterval,
		ReadBlock:               c.readBlock,
		FatalOnGroupCreateError: true,
		Handle:                  c.handle,
	}
	runner.Run(ctx)
}

func (c *ItemStatsConsumer) handle(ctx context.Context, msgID string, values map[string]any) HandleResult {
	event, err := itemstats.ParseEvent(values)
	if err != nil {
		logger.Default().Warn("ItemStatsConsumer invalid message", "err", err)
		return HandleFailure
	}

	if err := c.handleEvent(ctx, msgID, event); err != nil {
		logger.Default().Error("ItemStatsConsumer failed to process event", "eventType", event.EventType, "itemID", event.ItemID, "err", err)
		return HandleRetry
	}

	logger.Default().Info("ItemStatsConsumer processed event", "eventType", event.EventType, "itemID", event.ItemID)
	return HandleSuccess
}

func (c *ItemStatsConsumer) handleEventDefault(ctx context.Context, msgID string, event itemstats.Event) error {
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
		inserted, err := c.persistFeedbackEvent(msgID, event)
		if err != nil {
			return err
		}
		if !inserted {
			logger.Default().Info("ItemStatsConsumer deduplicated feedback log", "msgID", msgID, "itemID", event.ItemID)
			return nil
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

func (c *ItemStatsConsumer) persistFeedbackEvent(msgID string, event itemstats.Event) (bool, error) {
	now := nowFeedbackLogMs()
	entry := newFeedbackLog(msgID, feedbackEvent{
		agentID:      event.AgentID,
		itemID:       event.ItemID,
		score:        event.Score,
		impressionID: event.ImpressionID,
	}, now)

	inserted := false
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		var insertErr error
		inserted, insertErr = insertFeedbackLog(tx, entry)
		if insertErr != nil {
			return insertErr
		}
		if !inserted {
			return nil
		}
		return itemdal.IncrementItemScore(tx, event.ItemID, event.Score)
	})
	if err != nil {
		return false, err
	}
	return inserted, nil
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
