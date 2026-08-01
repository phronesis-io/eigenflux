package consumer

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/mq"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupItemConsumerRedis points mq.RDB at an in-process miniredis for the
// duration of the test.
func setupItemConsumerRedis(t *testing.T) {
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

// newTestItemConsumer builds an ItemConsumer with the production retry
// configuration but test-sized timings, and replaces the message handler.
// It deliberately does NOT override maxRetries: the delivery guarantee under
// test is the one NewItemConsumer ships with.
func newTestItemConsumer(t *testing.T, name string, handle MessageHandler) *ItemConsumer {
	t.Helper()

	c := NewItemConsumer(&config.Config{ItemConsumerWorkers: 1}, nil)
	c.consumerName = name
	c.retryMinIdle = 5 * time.Millisecond
	c.readBlock = 10 * time.Millisecond
	c.handleMessage = handle
	return c
}

// runItemConsumer starts the consumer and returns a stop function.
func runItemConsumer(t *testing.T, c *ItemConsumer) func() {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.Start(ctx)
		close(done)
	}()
	return func() {
		cancel()
		<-done
	}
}

func publishItem(t *testing.T, itemID string) {
	t.Helper()
	_, err := mq.Publish(context.Background(), itemStream, map[string]any{"item_id": itemID})
	require.NoError(t, err)
}

func pendingCount(t *testing.T) int64 {
	t.Helper()
	n, err := mq.PendingCount(context.Background(), itemStream, itemGroup)
	require.NoError(t, err)
	return n
}

// TestItemConsumerReclaimsMessageAfterTransientRetry is the regression test for
// the enrichment-reliability bug.
//
// A single transient failure (in production: a failed UpdateProcessedItemStatus
// write) makes handle() return HandleRetry. StreamConsumer deliberately skips
// the ACK for that result. With MaxRetries == 0 the runner only ever calls
// mq.Consume (XREADGROUP ">"), which reads *new* entries and never reclaims the
// pending-entries list — so the message stays in the PEL forever and the item
// is never enriched.
//
// Before the fix this test fails: the retry is never redelivered, so attempts
// stays at 1 and the PEL never drains.
func TestItemConsumerReclaimsMessageAfterTransientRetry(t *testing.T) {
	setupItemConsumerRedis(t)

	var attempts atomic.Int64
	c := newTestItemConsumer(t, "test-item-retry-recovers",
		func(ctx context.Context, msgID string, values map[string]any) HandleResult {
			if attempts.Add(1) == 1 {
				return HandleRetry
			}
			return HandleSuccess
		})

	stop := runItemConsumer(t, c)
	defer stop()

	publishItem(t, "1001")

	require.Eventuallyf(t, func() bool {
		return attempts.Load() >= 2 && pendingCount(t) == 0
	}, 5*time.Second, 10*time.Millisecond,
		"message returning HandleRetry was never reclaimed and redelivered")

	assert.EqualValues(t, 0, pendingCount(t), "pending entries list must drain after a successful retry")
}

// TestItemConsumerRetryIsBounded pins the upper bound on redelivery: a handler
// that never succeeds must not be retried forever. Once the retry count reaches
// MaxRetries the runner drops the message and ACKs it, so the PEL drains.
func TestItemConsumerRetryIsBounded(t *testing.T) {
	setupItemConsumerRedis(t)

	var attempts atomic.Int64
	c := newTestItemConsumer(t, "test-item-retry-bounded",
		func(ctx context.Context, msgID string, values map[string]any) HandleResult {
			attempts.Add(1)
			return HandleRetry
		})

	stop := runItemConsumer(t, c)
	defer stop()

	publishItem(t, "1002")

	// Wait for the message to actually be delivered and retried at least once
	// before checking that it was dropped — an empty PEL on its own is also the
	// state before first delivery.
	require.Eventuallyf(t, func() bool {
		return attempts.Load() >= 2 && pendingCount(t) == 0
	}, 5*time.Second, 10*time.Millisecond,
		"permanently failing message was never retried and then dropped after MaxRetries")

	observed := attempts.Load()
	assert.LessOrEqual(t, observed, itemMaxRetryCount+1,
		"handler ran %d times, expected at most MaxRetries+1 (%d)", observed, itemMaxRetryCount+1)
	assert.Greater(t, observed, int64(1), "a permanently failing message should be retried at least once")

	// The drop must be terminal: no further redelivery after the PEL drains.
	settled := attempts.Load()
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, settled, attempts.Load(), "dropped message must not be redelivered again")
	assert.EqualValues(t, 0, pendingCount(t))
}

// TestItemConsumerSuccessPathAcksExactlyOnce guards against the retry change
// introducing duplicate delivery on the happy path.
func TestItemConsumerSuccessPathAcksExactlyOnce(t *testing.T) {
	setupItemConsumerRedis(t)

	var attempts atomic.Int64
	c := newTestItemConsumer(t, "test-item-success-once",
		func(ctx context.Context, msgID string, values map[string]any) HandleResult {
			attempts.Add(1)
			return HandleSuccess
		})

	stop := runItemConsumer(t, c)
	defer stop()

	publishItem(t, "1003")

	require.Eventually(t, func() bool {
		return attempts.Load() == 1 && pendingCount(t) == 0
	}, 5*time.Second, 10*time.Millisecond)

	time.Sleep(200 * time.Millisecond)
	assert.EqualValues(t, 1, attempts.Load(), "a successful message must be processed exactly once")
	assert.EqualValues(t, 0, pendingCount(t))
}

// TestItemConsumerFailureIsAckedNotRetried documents the distinction the fix
// preserves: HandleFailure is a poison-message verdict and is ACKed and
// dropped immediately, unlike HandleRetry.
func TestItemConsumerFailureIsAckedNotRetried(t *testing.T) {
	setupItemConsumerRedis(t)

	var attempts atomic.Int64
	c := newTestItemConsumer(t, "test-item-failure-dropped",
		func(ctx context.Context, msgID string, values map[string]any) HandleResult {
			attempts.Add(1)
			return HandleFailure
		})

	stop := runItemConsumer(t, c)
	defer stop()

	publishItem(t, "1004")

	require.Eventually(t, func() bool {
		return attempts.Load() == 1 && pendingCount(t) == 0
	}, 5*time.Second, 10*time.Millisecond)

	time.Sleep(200 * time.Millisecond)
	assert.EqualValues(t, 1, attempts.Load(), "HandleFailure must not be redelivered")
	assert.EqualValues(t, 0, pendingCount(t))
}
