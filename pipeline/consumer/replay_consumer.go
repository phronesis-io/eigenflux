package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/replaylog"
)

const (
	replayBatchSize         = int64(100)
	replayMaxRetryCount     = int64(3)
	replayRetryMinIdle      = time.Second
	replayRetryPollInterval = 200 * time.Millisecond
	replayReadBlock         = 500 * time.Millisecond
	replayMaxWorkers        = 5
)

type ReplayConsumer struct {
	idGen        *idgen.ManagedGenerator
	consumerName string
}

func NewReplayConsumer(idGen *idgen.ManagedGenerator) *ReplayConsumer {
	hostname, _ := os.Hostname()
	name := fmt.Sprintf("replay-worker-%s-%d", hostname, os.Getpid())
	return &ReplayConsumer{idGen: idGen, consumerName: name}
}

func (c *ReplayConsumer) Start(ctx context.Context) {
	logger.Default().Info("ReplayConsumer starting", "workers", replayMaxWorkers)

	if err := mq.EnsureConsumerGroup(ctx, replaylog.StreamName, replaylog.GroupName); err != nil {
		logger.Default().Error("ReplayConsumer failed to create consumer group", "err", err)
		return
	}

	type msgTask struct {
		id     string
		values map[string]interface{}
	}
	msgChan := make(chan msgTask, replayMaxWorkers*2)
	var wg sync.WaitGroup

	for i := 0; i < replayMaxWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range msgChan {
				c.processMessage(ctx, task.id, task.values)
			}
		}()
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				close(msgChan)
				return
			default:
			}

			msgs, err := c.nextBatch(ctx)
			if err != nil {
				logger.Default().Error("ReplayConsumer consume error", "err", err)
				time.Sleep(time.Second)
				continue
			}

			for _, msg := range msgs {
				task := msgTask{id: msg.Message.ID, values: msg.Message.Values}
				select {
				case msgChan <- task:
				case <-ctx.Done():
					close(msgChan)
					return
				}
			}
		}
	}()

	<-ctx.Done()
	logger.Default().Info("ReplayConsumer shutting down, waiting for workers...")
	wg.Wait()
	logger.Default().Info("ReplayConsumer all workers stopped")
}

func (c *ReplayConsumer) nextBatch(ctx context.Context) ([]mq.PendingMessage, error) {
	reclaimed, err := mq.ConsumePending(ctx, replaylog.StreamName, replaylog.GroupName, c.consumerName, replayBatchSize, replayRetryMinIdle)
	if err != nil {
		return nil, err
	}
	if len(reclaimed) > 0 {
		msgs := make([]mq.PendingMessage, 0, len(reclaimed))
		for _, pending := range reclaimed {
			if pending.RetryCount >= replayMaxRetryCount {
				logger.Default().Warn("ReplayConsumer dropping message after max retries", "msgID", pending.Message.ID, "retryCount", pending.RetryCount)
				c.ackMessage(ctx, pending.Message.ID)
				continue
			}
			msgs = append(msgs, pending)
		}
		if len(msgs) > 0 {
			return msgs, nil
		}
	}

	pendingCount, err := mq.PendingCount(ctx, replaylog.StreamName, replaylog.GroupName)
	if err != nil {
		return nil, err
	}
	if pendingCount > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(replayRetryPollInterval):
			return nil, nil
		}
	}

	messages, err := mq.ConsumeWithBlock(ctx, replaylog.StreamName, replaylog.GroupName, c.consumerName, replayBatchSize, replayReadBlock)
	if err != nil {
		return nil, err
	}

	msgs := make([]mq.PendingMessage, 0, len(messages))
	for _, message := range messages {
		msgs = append(msgs, mq.PendingMessage{Message: message})
	}
	return msgs, nil
}

func (c *ReplayConsumer) processMessage(ctx context.Context, msgID string, values map[string]interface{}) {
	agentIDStr, _ := values["agent_id"].(string)
	agentID, err := strconv.ParseInt(agentIDStr, 10, 64)
	if err != nil {
		logger.Default().Warn("ReplayConsumer invalid agent_id", "raw", agentIDStr, "err", err)
		c.ackMessage(ctx, msgID)
		return
	}

	impressionID, _ := values["impression_id"].(string)
	if impressionID == "" {
		logger.Default().Warn("ReplayConsumer invalid impression_id", "msgID", msgID)
		c.ackMessage(ctx, msgID)
		return
	}

	agentFeatures, _ := values["agent_features"].(string)
	if agentFeatures == "" {
		agentFeatures = "{}"
	}

	servedAtStr, _ := values["served_at"].(string)
	servedAt, _ := strconv.ParseInt(servedAtStr, 10, 64)

	itemsStr, _ := values["items"].(string)
	var servedItems []replaylog.ServedItem
	if err := json.Unmarshal([]byte(itemsStr), &servedItems); err != nil {
		logger.Default().Warn("ReplayConsumer invalid items JSON", "err", err)
		c.ackMessage(ctx, msgID)
		return
	}

	if len(servedItems) == 0 {
		c.ackMessage(ctx, msgID)
		return
	}

	now := nowMs()
	logs := make([]ReplayLog, 0, len(servedItems))
	for _, si := range servedItems {
		rowID, err := c.idGen.NextID()
		if err != nil {
			logger.Default().Error("ReplayConsumer failed to generate row id", "err", err)
			return
		}

		itemFeatures := si.ItemFeatures
		if itemFeatures == "" {
			itemFeatures = "{}"
		}

		score := si.Score
		logs = append(logs, ReplayLog{
			ID:            rowID,
			ImpressionID:  impressionID,
			AgentID:       agentID,
			ItemID:        si.ItemID,
			AgentFeatures: agentFeatures,
			ItemFeatures:  itemFeatures,
			ItemScore:     &score,
			Position:      si.Position,
			ServedAt:      servedAt,
			CreatedAt:     now,
		})
	}

	if err := batchInsertReplayLogs(db.DB, logs); err != nil {
		logger.Default().Error("ReplayConsumer failed to insert replay logs", "err", err, "count", len(logs))
		return
	}

	logger.Default().Info("ReplayConsumer inserted replay logs", "impressionID", impressionID, "count", len(logs))
	c.ackMessage(ctx, msgID)
}

func (c *ReplayConsumer) ackMessage(ctx context.Context, msgID string) {
	if err := mq.Ack(ctx, replaylog.StreamName, replaylog.GroupName, msgID); err != nil {
		logger.Default().Warn("ReplayConsumer failed to ack", "msgID", msgID, "err", err)
	}
}
