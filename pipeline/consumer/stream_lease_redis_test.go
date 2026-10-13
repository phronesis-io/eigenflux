package consumer

import (
	"context"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"eigenflux_server/pkg/mq"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func nativeLeaseRedis(t *testing.T) (*redis.Client, string) {
	t.Helper()
	addr := os.Getenv("PUBLISH_REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("PUBLISH_REDIS_TEST_ADDR must name an isolated loopback Redis")
	}
	host, _, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	require.Contains(t, []string{"127.0.0.1", "localhost", "::1"}, host)
	rdb := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1})
	require.NoError(t, rdb.Ping(context.Background()).Err())
	old := mq.RDB
	mq.RDB = rdb
	stream := "test:pgc-lease:" + uuid.NewString()
	t.Cleanup(func() {
		require.NoError(t, rdb.Del(context.Background(), stream, stream+":dlq", stream+":dlq:seen").Err())
		mq.RDB = old
		require.NoError(t, rdb.Close())
	})
	require.NoError(t, mq.EnsureConsumerGroup(context.Background(), stream, "lease-test"))
	return rdb, stream
}

func leaseRunner(stream string, handle MessageHandler, minIdle time.Duration) *StreamConsumer {
	return &StreamConsumer{Name: "LeaseTest", Stream: stream, Group: "lease-test", ConsumerName: "same-logical-name",
		MetricsLabel: "item", Workers: 1, BatchSize: 2, MaxRetries: 3, RetryMinIdle: minIdle,
		PollInterval: 5 * time.Millisecond, ReadBlock: 5 * time.Millisecond, DeadLetterStream: stream + ":dlq", Handle: handle}
}

func launchLeaseRunner(ctx context.Context, c *StreamConsumer) <-chan struct{} {
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	return done
}

func waitLeaseRunner(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("lease runner did not stop")
	}
}

func TestNativeRedisLeaseProtectsRunningAndLocallyQueuedWorkAcrossReplicas(t *testing.T) {
	rdb, stream := nativeLeaseRedis(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var active, maxActive atomic.Int32
	handle := func(ctx context.Context, id string, values map[string]any) HandleResult {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maxActive.Load(); n > old; old = maxActive.Load() {
			if maxActive.CompareAndSwap(old, n) {
				break
			}
		}
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return HandleRetry
			}
		}
		return HandleSuccess
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, id := range []string{"first", "queued"} {
		require.NoError(t, rdb.XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: map[string]any{"item_id": id}}).Err())
	}
	first := launchLeaseRunner(ctx, leaseRunner(stream, handle, 150*time.Millisecond))
	t.Cleanup(func() { cancel(); waitLeaseRunner(t, first) })
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first handler did not start")
	}
	second := launchLeaseRunner(ctx, leaseRunner(stream, handle, 150*time.Millisecond))
	t.Cleanup(func() { cancel(); waitLeaseRunner(t, second) })
	// Hold the barrier through several crash-detection windows. Redis PEL
	// state, rather than goroutine race detection, proves retry budget stays 1.
	time.Sleep(550 * time.Millisecond)
	pending, err := rdb.XPendingExt(ctx, &redis.XPendingExtArgs{Stream: stream, Group: "lease-test", Start: "-", End: "+", Count: 10}).Result()
	require.NoError(t, err)
	require.Len(t, pending, 2)
	for _, message := range pending {
		require.Equal(t, int64(1), message.RetryCount)
	}
	require.Equal(t, int32(1), calls.Load(), "neither active nor locally queued work may be reclaimed")
	require.Zero(t, rdb.XLen(ctx, stream+":dlq").Val())
	close(release)
	require.Eventually(t, func() bool { return calls.Load() == 2 && rdb.XPending(ctx, stream, "lease-test").Val().Count == 0 }, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, int32(1), maxActive.Load())
	cancel()
	waitLeaseRunner(t, first)
	waitLeaseRunner(t, second)
}

func TestNativeRedisLostOwnerCancelsHandlerAndCannotAckOrDeadLetterNewOwner(t *testing.T) {
	rdb, stream := nativeLeaseRedis(t)
	entered, cancelled := make(chan string, 1), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := launchLeaseRunner(ctx, leaseRunner(stream, func(ctx context.Context, id string, _ map[string]any) HandleResult {
		entered <- id
		<-ctx.Done()
		close(cancelled)
		return HandleFailure
	}, time.Second))
	t.Cleanup(func() { cancel(); waitLeaseRunner(t, runner) })
	require.NoError(t, rdb.XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: map[string]any{"item_id": "handoff"}}).Err())
	var id string
	select {
	case id = <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}
	initial, err := rdb.XPendingExt(ctx, &redis.XPendingExtArgs{Stream: stream, Group: "lease-test", Start: id, End: id, Count: 1}).Result()
	require.NoError(t, err)
	require.Len(t, initial, 1)
	oldOwner := initial[0].Consumer
	require.NoError(t, rdb.XClaim(ctx, &redis.XClaimArgs{Stream: stream, Group: "lease-test", Consumer: "replacement-owner", MinIdle: 0, Messages: []string{id}}).Err())
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("lost ownership did not cancel the handler")
	}
	cancel()
	waitLeaseRunner(t, runner)
	owned, err := mq.RefreshOwnedPending(context.Background(), stream, "lease-test", oldOwner, id)
	require.NoError(t, err)
	require.False(t, owned)
	acked, err := mq.AckOwned(context.Background(), stream, "lease-test", oldOwner, id)
	require.NoError(t, err)
	require.False(t, acked)
	dropped, err := mq.DeadLetterAndAckOwned(context.Background(), stream, "lease-test", oldOwner, id, stream+":dlq", map[string]any{
		"retry_count": 3, "payload": "{}", "payload_truncated": false, "failed_at": time.Now().UnixMilli(),
	})
	require.NoError(t, err)
	require.False(t, dropped)
	remaining, err := rdb.XPendingExt(context.Background(), &redis.XPendingExtArgs{Stream: stream, Group: "lease-test", Start: id, End: id, Count: 1}).Result()
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	require.Equal(t, "replacement-owner", remaining[0].Consumer)
	require.Zero(t, rdb.XLen(context.Background(), stream+":dlq").Val())
}

func TestNativeRedisInterruptedWorkerIsRecoveredByNextInstance(t *testing.T) {
	rdb, stream := nativeLeaseRedis(t)
	entered := make(chan struct{})
	firstCtx, stopFirst := context.WithCancel(context.Background())
	first := launchLeaseRunner(firstCtx, leaseRunner(stream, func(ctx context.Context, _ string, _ map[string]any) HandleResult {
		close(entered)
		<-ctx.Done()
		return HandleRetry
	}, 150*time.Millisecond))
	t.Cleanup(func() { stopFirst(); waitLeaseRunner(t, first) })
	require.NoError(t, rdb.XAdd(context.Background(), &redis.XAddArgs{Stream: stream, Values: map[string]any{"item_id": "restart-survivor"}}).Err())
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first worker did not start")
	}
	stopFirst()
	waitLeaseRunner(t, first)
	require.Equal(t, int64(1), rdb.XPending(context.Background(), stream, "lease-test").Val().Count)
	recovered := make(chan string, 1)
	nextCtx, stopNext := context.WithCancel(context.Background())
	next := launchLeaseRunner(nextCtx, leaseRunner(stream, func(_ context.Context, _ string, values map[string]any) HandleResult {
		recovered <- values["item_id"].(string)
		return HandleSuccess
	}, 150*time.Millisecond))
	t.Cleanup(func() { stopNext(); waitLeaseRunner(t, next) })
	select {
	case got := <-recovered:
		require.Equal(t, "restart-survivor", got)
	case <-time.After(2 * time.Second):
		t.Fatal("interrupted pending work was not reclaimed")
	}
	require.Eventually(t, func() bool { return rdb.XPending(context.Background(), stream, "lease-test").Val().Count == 0 }, time.Second, 10*time.Millisecond)
	require.Zero(t, rdb.XLen(context.Background(), stream+":dlq").Val())
	stopNext()
	waitLeaseRunner(t, next)
}
