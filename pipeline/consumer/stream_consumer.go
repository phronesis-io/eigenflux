package consumer

import (
	"context"
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
//     pending. Messages whose retry count has reached MaxRetries are ACKed
//     and dropped (with a ConsumerRetryTotal increment). Remaining messages
//     are dispatched alongside fresh ones read via mq.ConsumeWithBlock. In
//     this mode a HandleRetry result skips the ACK so the message stays
//     pending and will be reclaimed on the next poll. Matches the original
//     item-stats / replay consumers.
type StreamConsumer struct {
	// Name appears in log lines (e.g. "ServiceConsumer").
	Name string
	// Stream is the Redis Stream key, e.g. "stream:trade:service".
	Stream string
	// Group is the consumer-group name, e.g. "cg:trade:service".
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
	// this retry count are ACKed and dropped.
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

	// FatalOnGroupCreateError: when true (the default for trade/item/
	// profile/item-stats consumers), a failure to create the consumer
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
	msgChan := make(chan msgTask, workers*2)
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

				if result != HandleRetry {
					mq.Ack(ctx, c.Stream, c.Group, task.id)
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
	reclaimed, err := mq.ConsumePending(ctx, c.Stream, c.Group, c.ConsumerName, batch, minIdle)
	if err != nil {
		return nil, err
	}
	if len(reclaimed) > 0 {
		msgs := make([]mq.PendingMessage, 0, len(reclaimed))
		for _, pending := range reclaimed {
			if pending.RetryCount >= c.MaxRetries {
				logger.Default().Warn(c.Name+" dropping message after max retries",
					"msgID", pending.Message.ID, "retryCount", pending.RetryCount, "lastConsumer", pending.Consumer)
				metrics.ConsumerRetryTotal.WithLabelValues(c.MetricsLabel).Inc()
				mq.Ack(ctx, c.Stream, c.Group, pending.Message.ID)
				continue
			}
			msgs = append(msgs, pending)
		}
		if len(msgs) > 0 {
			return msgs, nil
		}
	}

	pendingCount, err := mq.PendingCount(ctx, c.Stream, c.Group)
	if err != nil {
		return nil, err
	}
	if pendingCount > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
			return nil, nil
		}
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
