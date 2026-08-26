package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"eigenflux_server/api/dal"
	"eigenflux_server/pkg/activity"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/mq"

	"gorm.io/gorm"
)

const (
	activityBatchSize    = int64(100)
	activityMaxRetry     = int64(3)
	activityRetryMinIdle = time.Second
	activityRetryPoll    = 200 * time.Millisecond
	activityReadBlock    = 500 * time.Millisecond
	activityMaxWorkers   = 5
	activityDLQStream    = "stream:agent:activity:dlq"
)

type ActivityConsumer struct {
	idGen        *idgen.ManagedGenerator
	consumerName string
}

func NewActivityConsumer(idGen *idgen.ManagedGenerator) *ActivityConsumer {
	hostname, _ := os.Hostname()
	name := fmt.Sprintf("activity-worker-%s-%d", hostname, os.Getpid())
	return &ActivityConsumer{idGen: idGen, consumerName: name}
}

func (c *ActivityConsumer) Start(ctx context.Context) {
	logger.Default().Info("ActivityConsumer starting", "workers", activityMaxWorkers)

	if err := mq.EnsureConsumerGroup(ctx, activity.StreamName, activity.GroupName); err != nil {
		logger.Default().Error("ActivityConsumer failed to create consumer group", "err", err)
		return
	}

	type msgTask struct {
		id     string
		values map[string]interface{}
	}
	msgChan := make(chan msgTask, activityMaxWorkers*2)
	var wg sync.WaitGroup

	for i := 0; i < activityMaxWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range msgChan {
				start := time.Now()
				c.processMessage(ctx, task.id, task.values)
				metrics.ConsumerMessageDuration.WithLabelValues("agent:activity").Observe(time.Since(start).Seconds())
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
				logger.Default().Error("ActivityConsumer consume error", "err", err)
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
	logger.Default().Info("ActivityConsumer shutting down, waiting for workers...")
	wg.Wait()
	logger.Default().Info("ActivityConsumer all workers stopped")
}

func (c *ActivityConsumer) nextBatch(ctx context.Context) ([]mq.PendingMessage, error) {
	reclaimed, err := mq.ConsumePending(ctx, activity.StreamName, activity.GroupName, c.consumerName, activityBatchSize, activityRetryMinIdle)
	if err != nil {
		return nil, err
	}
	if len(reclaimed) > 0 {
		msgs := make([]mq.PendingMessage, 0, len(reclaimed))
		for _, pending := range reclaimed {
			if pending.RetryCount >= activityMaxRetry {
				logger.Default().Warn("ActivityConsumer moving message to DLQ after max retries", "msgID", pending.Message.ID, "retryCount", pending.RetryCount)
				dlqValues := make(map[string]interface{}, len(pending.Message.Values)+2)
				for key, value := range pending.Message.Values {
					dlqValues[key] = value
				}
				dlqValues["source_message_id"] = pending.Message.ID
				dlqValues["retry_count"] = pending.RetryCount
				if _, publishErr := mq.Publish(ctx, activityDLQStream, dlqValues); publishErr != nil {
					logger.Default().Error("ActivityConsumer DLQ publish failed", "msgID", pending.Message.ID, "err", publishErr)
					continue
				}
				c.ackMessage(ctx, pending.Message.ID)
				continue
			}
			msgs = append(msgs, pending)
		}
		if len(msgs) > 0 {
			return msgs, nil
		}
	}

	pendingCount, err := mq.PendingCount(ctx, activity.StreamName, activity.GroupName)
	if err != nil {
		return nil, err
	}
	if pendingCount > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(activityRetryPoll):
			return nil, nil
		}
	}

	messages, err := mq.ConsumeWithBlock(ctx, activity.StreamName, activity.GroupName, c.consumerName, activityBatchSize, activityReadBlock)
	if err != nil {
		return nil, err
	}

	msgs := make([]mq.PendingMessage, 0, len(messages))
	for _, message := range messages {
		msgs = append(msgs, mq.PendingMessage{Message: message})
	}
	return msgs, nil
}

func (c *ActivityConsumer) processMessage(ctx context.Context, msgID string, values map[string]interface{}) {
	agentIDStr, _ := values["agent_id"].(string)
	agentID, err := strconv.ParseInt(agentIDStr, 10, 64)
	if err != nil {
		logger.Default().Warn("ActivityConsumer invalid agent_id", "raw", agentIDStr, "err", err)
		metrics.ConsumerMessagesTotal.WithLabelValues("agent:activity", "failure").Inc()
		c.ackMessage(ctx, msgID)
		return
	}

	eventType, _ := values["event_type"].(string)
	if !isSupportedActivityType(eventType) {
		logger.Default().Warn("ActivityConsumer missing event_type", "msgID", msgID)
		metrics.ConsumerMessagesTotal.WithLabelValues("agent:activity", "failure").Inc()
		c.ackMessage(ctx, msgID)
		return
	}

	summary, _ := values["summary"].(string)
	detail, _ := values["detail"].(string)
	// detail maps to a JSONB column; an empty string is invalid JSON and would
	// fail the insert, so default to an empty object.
	if detail == "" {
		detail = "{}"
	}

	logID, err := c.idGen.NextID()
	if err != nil {
		logger.Default().Error("ActivityConsumer failed to generate log_id", "err", err)
		return
	}

	createdAt := time.Now().UnixMilli()
	sourceEventID := activity.StreamName + ":" + msgID
	inserted := false
	if err := db.DB.Transaction(func(tx *gorm.DB) error {
		// Multiple workers may reclaim the same pending Redis entry. Serialize
		// only that source ID before allocating an Agent sequence, otherwise a
		// duplicate would consume a sequence and create a permanent SSE gap.
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, sourceEventID).Error; err != nil {
			return err
		}
		var exists bool
		if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM agent_activity_log WHERE source_event_id = ?)`, sourceEventID).Scan(&exists).Error; err != nil {
			return err
		}
		if exists {
			return nil
		}
		if err := tx.Exec(`INSERT INTO agent_activity_heads (agent_id, current_seq)
			VALUES (?, 0) ON CONFLICT (agent_id) DO NOTHING`, agentID).Error; err != nil {
			return err
		}
		var agentSeq int64
		if err := tx.Raw(`UPDATE agent_activity_heads SET current_seq = current_seq + 1
			WHERE agent_id = ? RETURNING current_seq`, agentID).Scan(&agentSeq).Error; err != nil {
			return err
		}
		var insertedLogID int64
		if err := tx.Raw(`INSERT INTO agent_activity_log
			(log_id, agent_id, event_type, summary, detail, created_at, agent_seq, source_event_id)
			VALUES (?, ?, ?, ?, ?::jsonb, ?, ?, ?)
			ON CONFLICT (source_event_id) WHERE source_event_id IS NOT NULL DO NOTHING RETURNING log_id`, logID, agentID, eventType,
			summary, detail, createdAt, agentSeq, sourceEventID).Scan(&insertedLogID).Error; err != nil {
			return err
		}
		inserted = insertedLogID != 0
		return nil
	}); err != nil {
		logger.Default().Error("ActivityConsumer failed to insert activity log", "err", err)
		metrics.ConsumerMessagesTotal.WithLabelValues("agent:activity", "failure").Inc()
		return
	}
	if !inserted {
		c.ackMessage(ctx, msgID)
		return
	}
	// Pub/Sub is only a low-latency hint. DB replay remains authoritative, so
	// a transient publish failure must not make the durable stream event fail.
	if err := mq.RDB.Publish(ctx, "console:v2:activity:wakeup", strconv.FormatInt(agentID, 10)).Err(); err != nil {
		logger.Default().Warn("ActivityConsumer V2 wakeup publish failed", "agentID", agentID, "err", err)
	}

	// Increment the all-time impression counter by the number of signals
	// delivered in this feed pull (carried in detail). Falls back to 1 if the
	// count is missing or unparseable.
	if eventType == "feed_pull" {
		delta := int64(1)
		if n := parseDetailInt(detail, "count"); n > 0 {
			delta = n
		}
		_ = dal.IncrImpressionCount(ctx, agentID, delta)
	}

	// Increment the all-time worth-reading counter by items kept (score>=1).
	if eventType == "feedback" {
		if kept := parseDetailInt(detail, "kept"); kept > 0 {
			_ = dal.IncrWorthCount(ctx, agentID, kept)
		}
	}

	metrics.ConsumerMessagesTotal.WithLabelValues("agent:activity", "success").Inc()
	c.ackMessage(ctx, msgID)
}

func isSupportedActivityType(value string) bool {
	switch value {
	case "feed_pull", "broadcast", "feedback", "message_sent", "message_received", "reply_received",
		"profile_update", "friend_request_sent", "friend_request_received", "friend_added",
		"agent_joined", "agent_card_update", "network_goal_update", "intent_actions_update", "onboarding_completed":
		return true
	default:
		return false
	}
}

// parseDetailInt extracts an integer field from a JSON detail string.
// Returns 0 if detail is empty, malformed, or the field is absent.
func parseDetailInt(detail, field string) int64 {
	if detail == "" {
		return 0
	}
	var m map[string]int64
	if err := json.Unmarshal([]byte(detail), &m); err != nil {
		return 0
	}
	return m[field]
}

func (c *ActivityConsumer) ackMessage(ctx context.Context, msgID string) {
	if err := mq.Ack(ctx, activity.StreamName, activity.GroupName, msgID); err != nil {
		logger.Default().Warn("ActivityConsumer failed to ack", "msgID", msgID, "err", err)
	}
}
