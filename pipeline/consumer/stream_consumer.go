package consumer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/mq"
)

// HandleResult tells the runner how to label the metric and whether to ACK the
// message after the handler returns.
type HandleResult int

const (
	// HandleSuccess: ack the message, emit success metric.
	HandleSuccess HandleResult = iota
	// HandleFailure: ack the message (drop / poison), emit failure metric.
	HandleFailure
	// HandleRetry: do NOT ack the message. Emits failure metric. The
	// message stays pending so it will be reclaimed on the next pass. Only
	// meaningful when MaxRetries is set; in simple mode (MaxRetries = 0)
	// this still skips the ACK, leaving the message pending until the
	// consumer-group expires or is reset.
	HandleRetry
)

// MessageHandler processes a single Redis Stream message. The runner has
// already dispatched it to a worker; the handler does NOT call mq.Ack — the
// runner decides whether to ack based on the returned HandleResult and the
// consumer's MaxRetries setting.
type MessageHandler func(ctx context.Context, msgID string, values map[string]any) HandleResult

// StreamConsumer is the shared worker-pool + dispatcher used by every Redis
// Streams consumer in this package. It owns EnsureConsumerGroup, the worker
// pool, the read loop, ACK, and metrics; callers supply only configuration
// and the per-message handler.
//
// Two delivery modes are supported:
//
//   - Simple mode (MaxRetries == 0): each poll calls mq.Consume and every
//     handled message is ACKed, regardless of HandleResult, unless the result
//     is HandleRetry. This matches the behavior of the original service /
//     order-event / item / profile consumers.
//
//   - Retry-aware mode (MaxRetries > 0): each poll first calls
//     mq.ConsumePending to reclaim messages that previous workers left
//     pending. Messages whose retry count has reached MaxRetries are copied
//     to DeadLetterStream when configured, then ACKed. Remaining messages
//     are dispatched alongside fresh ones read via mq.ConsumeWithBlock. In
//     this mode a HandleRetry result skips the ACK so the message stays
//     pending and will be reclaimed on the next poll. Matches the original
//     item-stats / replay consumers.
type StreamConsumer struct {
	// Name appears in log lines (e.g. "ServiceConsumer").
	Name string
	// Stream is the Redis Stream key, e.g. "stream:item:publish".
	Stream string
	// Group is the consumer-group name, e.g. "cg:item:publish".
	Group string
	// ConsumerName is the per-instance consumer name reported to Redis.
	ConsumerName string
	// MetricsLabel is the label used for metrics.ConsumerMessagesTotal,
	// metrics.ConsumerMessageDuration, and metrics.ConsumerRetryTotal.
	MetricsLabel string
	// Workers is the size of the worker pool. Defaults to 2.
	Workers int
	// BatchSize is how many messages each XREADGROUP call requests.
	// Defaults to 10.
	BatchSize int64

	// MaxRetries > 0 switches to retry-aware mode. Messages that exceed
	// this retry count are dead-lettered when configured, then ACKed.
	MaxRetries int64
	// RetryMinIdle is the minimum idle time before a pending message is
	// eligible for reclaim. Defaults to 1s when in retry mode.
	RetryMinIdle time.Duration
	// PollInterval is how long the dispatcher waits when pending messages
	// remain in-flight on other consumers. Defaults to 200ms.
	PollInterval time.Duration
	// ReadBlock is the XREADGROUP BLOCK timeout for fresh reads in retry
	// mode. Defaults to 500ms.
	ReadBlock time.Duration
	// DeadLetterStream receives a bounded copy before a message that exhausted
	// retries is ACKed. Empty preserves the legacy drop-only behavior.
	DeadLetterStream string
	// UnbufferedDispatch prevents messages from aging in a local queue before
	// their handler starts. Use it when RetryMinIdle is the crash detector.
	UnbufferedDispatch bool
	preferFresh        bool

	// FatalOnGroupCreateError: when true (the default for item/profile/item-stats
	// consumers), a failure to create the consumer
	// group calls os.Exit(1). Set to false to log and return — this
	// matches ReplayConsumer's prior behavior.
	FatalOnGroupCreateError bool

	// Handle processes one message. Required.
	Handle MessageHandler
}

// Run starts the consumer and blocks until ctx is cancelled.
func (c *StreamConsumer) Run(ctx context.Context) {
	workers := c.Workers
	if workers <= 0 {
		workers = 2
	}
	batch := c.BatchSize
	if batch <= 0 {
		batch = 10
	}
	retryMinIdle := c.RetryMinIdle
	if retryMinIdle <= 0 {
		retryMinIdle = time.Second
	}
	pollInterval := c.PollInterval
	if pollInterval <= 0 {
		pollInterval = 200 * time.Millisecond
	}
	readBlock := c.ReadBlock
	if readBlock <= 0 {
		readBlock = 500 * time.Millisecond
	}

	logger.Default().Info(c.Name+" starting",
		"workers", workers, "stream", c.Stream, "group", c.Group, "maxRetries", c.MaxRetries)

	if err := mq.EnsureConsumerGroup(ctx, c.Stream, c.Group); err != nil {
		logger.Default().Error(c.Name+" failed to create consumer group", "err", err)
		if c.FatalOnGroupCreateError {
			os.Exit(1)
		}
		return
	}

	type msgTask struct {
		id     string
		values map[string]any
	}
	queueSize := workers * 2
	if c.UnbufferedDispatch {
		queueSize = 0
	}
	msgChan := make(chan msgTask, queueSize)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			logger.Default().Info(c.Name+" worker started", "workerID", workerID)
			for task := range msgChan {
				start := time.Now()
				result := c.Handle(ctx, task.id, task.values)
				metrics.ConsumerMessageDuration.WithLabelValues(c.MetricsLabel).Observe(time.Since(start).Seconds())

				status := "success"
				if result != HandleSuccess {
					status = "failure"
				}
				metrics.ConsumerMessagesTotal.WithLabelValues(c.MetricsLabel, status).Inc()

				if result == HandleFailure && c.DeadLetterStream != "" {
					if err := c.deadLetterAndAck(ctx, task.id, task.values, 0); err != nil {
						logger.Default().Error(c.Name+" poison-message DLQ failed", "msgID", task.id, "err", err)
					}
				} else if result != HandleRetry {
					if err := mq.Ack(ctx, c.Stream, c.Group, task.id); err != nil {
						logger.Default().Error(c.Name+" ACK failed", "msgID", task.id, "err", err)
					}
				}
			}
			logger.Default().Info(c.Name+" worker stopped", "workerID", workerID)
		}(i)
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				logger.Default().Info(c.Name + " context cancelled, closing message channel")
				close(msgChan)
				return
			default:
			}

			var msgs []mq.PendingMessage
			var err error
			if c.MaxRetries > 0 {
				msgs, err = c.nextBatchWithRetry(ctx, batch, retryMinIdle, pollInterval, readBlock)
			} else {
				msgs, err = c.nextBatchSimple(ctx, batch)
			}
			if err != nil {
				logger.Default().Error(c.Name+" consume error", "err", err)
				time.Sleep(time.Second)
				continue
			}

			for _, msg := range msgs {
				select {
				case msgChan <- msgTask{id: msg.Message.ID, values: msg.Message.Values}:
				case <-ctx.Done():
					logger.Default().Info(c.Name + " context cancelled while sending message")
					close(msgChan)
					return
				}
			}
		}
	}()

	<-ctx.Done()
	logger.Default().Info(c.Name + " shutting down, waiting for workers to finish...")
	wg.Wait()
	logger.Default().Info(c.Name + " all workers stopped")
}

func (c *StreamConsumer) nextBatchSimple(ctx context.Context, batch int64) ([]mq.PendingMessage, error) {
	raw, err := mq.Consume(ctx, c.Stream, c.Group, c.ConsumerName, batch)
	if err != nil {
		return nil, err
	}
	out := make([]mq.PendingMessage, 0, len(raw))
	for _, m := range raw {
		out = append(out, mq.PendingMessage{Message: m})
	}
	return out, nil
}

func (c *StreamConsumer) nextBatchWithRetry(ctx context.Context, batch int64, minIdle, pollInterval, readBlock time.Duration) ([]mq.PendingMessage, error) {
	var reclaimed []mq.PendingMessage
	var err error
	if !c.preferFresh {
		reclaimed, err = mq.ConsumePending(ctx, c.Stream, c.Group, c.ConsumerName, batch, minIdle)
	}
	if err != nil {
		return nil, err
	}
	if len(reclaimed) > 0 {
		msgs := make([]mq.PendingMessage, 0, len(reclaimed))
		for _, pending := range reclaimed {
			if pending.RetryCount >= c.MaxRetries {
				logger.Default().Warn(c.Name+" dropping message after max retries",
					"msgID", pending.Message.ID, "retryCount", pending.RetryCount, "lastConsumer", pending.Consumer)
				if c.DeadLetterStream != "" {
					if dlqErr := c.deadLetterAndAck(ctx, pending.Message.ID, pending.Message.Values, pending.RetryCount); dlqErr != nil {
						return nil, dlqErr
					}
				} else if ackErr := mq.Ack(ctx, c.Stream, c.Group, pending.Message.ID); ackErr != nil {
					return nil, fmt.Errorf("ACK exhausted message %s: %w", pending.Message.ID, ackErr)
				}
				metrics.ConsumerRetryTotal.WithLabelValues(c.MetricsLabel).Inc()
				continue
			}
			msgs = append(msgs, pending)
		}
		if len(msgs) > 0 {
			c.preferFresh = true
			return msgs, nil
		}
	}
	c.preferFresh = false

	pendingCount, err := mq.PendingCount(ctx, c.Stream, c.Group)
	if err != nil {
		return nil, err
	}
	if pendingCount > 0 && readBlock > pollInterval {
		// Pending messages may still be actively handled by another worker or
		// replica. Continue admitting fresh work, but poll briefly so pending
		// retries are revisited without imposing global head-of-line blocking.
		readBlock = pollInterval
	}

	messages, err := mq.ConsumeWithBlock(ctx, c.Stream, c.Group, c.ConsumerName, batch, readBlock)
	if err != nil {
		return nil, err
	}
	msgs := make([]mq.PendingMessage, 0, len(messages))
	for _, message := range messages {
		msgs = append(msgs, mq.PendingMessage{Message: message})
	}
	return msgs, nil
}

const deadLetterPayloadLimit = 16 * 1024

func (c *StreamConsumer) deadLetterAndAck(ctx context.Context, id string, values map[string]any, retryCount int64) error {
	safe := make(map[string]string)
	truncated := false
	for key, value := range values {
		if len(safe) >= 32 {
			truncated = true
			break
		}
		if len(key) > 128 {
			key = key[:128]
			truncated = true
		}
		rendered := fmt.Sprint(value)
		if len(rendered) > 1024 {
			sum := sha256.Sum256([]byte(rendered))
			rendered = rendered[:1024] + fmt.Sprintf("...[sha256:%x]", sum)
			truncated = true
		}
		safe[key] = rendered
	}
	payload, err := json.Marshal(safe)
	if err != nil {
		return fmt.Errorf("marshal dead letter %s: %w", id, err)
	}
	if len(payload) > deadLetterPayloadLimit {
		sum := sha256.Sum256(payload)
		payload = []byte(fmt.Sprintf(`{"sha256":"%x","note":"payload exceeded limit"}`, sum))
		truncated = true
	}
	return mq.DeadLetterAndAck(ctx, c.Stream, c.Group, id, c.DeadLetterStream, map[string]interface{}{
		"retry_count":       retryCount,
		"payload":           string(payload),
		"payload_truncated": truncated,
		"failed_at":         time.Now().UnixMilli(),
	})
}
