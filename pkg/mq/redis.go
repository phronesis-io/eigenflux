package mq

import (
	"context"
	"eigenflux_server/pkg/logger"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

var RDB *redis.Client

type PendingMessage struct {
	Message    redis.XMessage
	RetryCount int64
	Consumer   string
}

func Init(addr, password string) {
	RDB = redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := RDB.Ping(ctx).Err(); err != nil {
		logger.Default().Error("failed to connect to redis", "err", err)
		os.Exit(1)
	}
}

// defaultStreamMaxLen is the approximate length cap applied by Publish to every
// stream except those in streamsExemptFromCap. XACK only clears a message from
// the consumer group PEL; it never removes the entry from the stream, so
// without a cap streams grow until Redis OOMs. Override via SetDefaultStreamMaxLen.
var defaultStreamMaxLen int64 = 20000

// streamsExemptFromCap lists ingestion streams that must stay unbounded. MAXLEN
// trims by entry ID regardless of consumer-group pending state, so a producer
// that legitimately bursts far ahead of its consumer (bulk requeue/backfill on
// these streams) would have its unconsumed entries silently trimmed and lost.
// Their memory is instead reclaimed by a consumed-offset (MINID) trim, not here.
var streamsExemptFromCap = map[string]bool{
	"stream:item:publish":   true,
	"stream:profile:update": true,
}

// SetDefaultStreamMaxLen overrides the cap applied by Publish. A non-positive
// value disables capping for all non-exempt streams.
func SetDefaultStreamMaxLen(n int64) {
	defaultStreamMaxLen = n
}

// Publish sends a message to a Redis Stream, applying defaultStreamMaxLen unless
// the stream is exempt (ingestion streams that must not lose unconsumed entries).
func Publish(ctx context.Context, stream string, values map[string]interface{}) (string, error) {
	maxLen := defaultStreamMaxLen
	if streamsExemptFromCap[stream] {
		maxLen = 0
	}
	return PublishCapped(ctx, stream, maxLen, values)
}

// PublishCapped sends a message with an explicit approximate length bound,
// bypassing the exempt list and default cap. MaxLen with Approx lets Redis trim
// old entries cheaply at insert time. A non-positive maxLen writes unbounded.
func PublishCapped(ctx context.Context, stream string, maxLen int64, values map[string]interface{}) (string, error) {
	if RDB == nil {
		return "", fmt.Errorf("redis is not initialized")
	}
	args := &redis.XAddArgs{Stream: stream, Values: values}
	if maxLen > 0 {
		args.MaxLen = maxLen
		args.Approx = true
	}
	return RDB.XAdd(ctx, args).Result()
}

// EnsureConsumerGroup creates a consumer group if it doesn't exist
func EnsureConsumerGroup(ctx context.Context, stream, group string) error {
	err := RDB.XGroupCreateMkStream(ctx, stream, group, "0").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		return err
	}
	return nil
}

// Consume reads messages from a Redis Stream consumer group
func Consume(ctx context.Context, stream, group, consumer string, count int64) ([]redis.XMessage, error) {
	return ConsumeWithBlock(ctx, stream, group, consumer, count, 5*time.Second)
}

func ConsumeWithBlock(ctx context.Context, stream, group, consumer string, count int64, block time.Duration) ([]redis.XMessage, error) {
	results, err := RDB.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    group,
		Consumer: consumer,
		Streams:  []string{stream, ">"},
		Count:    count,
		Block:    block,
	}).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}
	return results[0].Messages, nil
}

func PendingCount(ctx context.Context, stream, group string) (int64, error) {
	result, err := RDB.XPending(ctx, stream, group).Result()
	if err != nil {
		if err == redis.Nil {
			return 0, nil
		}
		return 0, err
	}
	return result.Count, nil
}

func ConsumePending(ctx context.Context, stream, group, consumer string, count int64, minIdle time.Duration) ([]PendingMessage, error) {
	return ConsumePendingExcept(ctx, stream, group, consumer, count, minIdle, nil)
}

// ConsumePendingExcept does not reclaim messages already queued or running in
// this instance. Other instances are protected by their PEL heartbeat.
func ConsumePendingExcept(ctx context.Context, stream, group, consumer string, count int64, minIdle time.Duration, active map[string]bool) ([]PendingMessage, error) {
	pendingEntries, err := RDB.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: stream,
		Group:  group,
		Start:  "-",
		End:    "+",
		Count:  count,
		Idle:   minIdle,
	}).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}
	if len(pendingEntries) == 0 {
		return nil, nil
	}

	ids := make([]string, 0, len(pendingEntries))
	metaByID := make(map[string]redis.XPendingExt, len(pendingEntries))
	for _, entry := range pendingEntries {
		if active[entry.ID] {
			continue
		}
		ids = append(ids, entry.ID)
		metaByID[entry.ID] = entry
	}
	if len(ids) == 0 {
		return nil, nil
	}

	messages, err := RDB.XClaim(ctx, &redis.XClaimArgs{
		Stream:   stream,
		Group:    group,
		Consumer: consumer,
		MinIdle:  minIdle,
		Messages: ids,
	}).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}

	claimed := make([]PendingMessage, 0, len(messages))
	for _, message := range messages {
		meta, ok := metaByID[message.ID]
		if !ok {
			continue
		}
		claimed = append(claimed, PendingMessage{
			Message:    message,
			RetryCount: meta.RetryCount,
			Consumer:   meta.Consumer,
		})
	}

	return claimed, nil
}

// Ack acknowledges a message
func Ack(ctx context.Context, stream, group, id string) error {
	return RDB.XAck(ctx, stream, group, id).Err()
}

// RefreshOwnedPending resets idle time without consuming retry budget. The
// owner check and JUSTID claim are atomic; a stale worker cannot steal a lease.
func RefreshOwnedPending(ctx context.Context, stream, group, owner, id string) (bool, error) {
	const script = `
local p = redis.call('XPENDING', KEYS[1], ARGV[1], ARGV[3], ARGV[3], 1)
if #p == 0 or p[1][2] ~= ARGV[2] then return 0 end
redis.call('XCLAIM', KEYS[1], ARGV[1], ARGV[2], 0, ARGV[3], 'RETRYCOUNT', p[1][4], 'JUSTID')
return 1`
	client := RDB
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, context.DeadlineExceeded
		}
		// go-redis does not enable context-based socket deadlines by default.
		client = client.WithTimeout(remaining)
	}
	n, err := client.Eval(ctx, script, []string{stream}, group, owner, id).Int()
	return n == 1, err
}

func AckOwned(ctx context.Context, stream, group, owner, id string) (bool, error) {
	const script = `
local p = redis.call('XPENDING', KEYS[1], ARGV[1], ARGV[3], ARGV[3], 1)
if #p == 0 or p[1][2] ~= ARGV[2] then return 0 end
return redis.call('XACK', KEYS[1], ARGV[1], ARGV[3])`
	n, err := RDB.Eval(ctx, script, []string{stream}, group, owner, id).Int()
	return n == 1, err
}

// DeadLetterAndAck writes one bounded diagnostic record before ACKing the
// source message. A bounded sorted set makes retries idempotent when the Lua
// reply is lost after Redis committed the script without creating one Redis
// key per failed message.
func DeadLetterAndAck(ctx context.Context, stream, group, id, deadLetterStream string, values map[string]interface{}) error {
	_, err := DeadLetterAndAckOwned(ctx, stream, group, "", id, deadLetterStream, values)
	return err
}

// DeadLetterAndAckOwned fences both external effects with the current PEL owner.
// An empty owner retains the legacy unfenced helper contract.
func DeadLetterAndAckOwned(ctx context.Context, stream, group, owner, id, deadLetterStream string, values map[string]interface{}) (bool, error) {
	markerKey := deadLetterStream + ":seen"
	marker := fmt.Sprintf("%s|%s|%s", stream, group, id)
	const script = `
if ARGV[8] ~= '' then
  local p = redis.call('XPENDING', KEYS[1], ARGV[1], ARGV[2], ARGV[2], 1)
  if #p == 0 or p[1][2] ~= ARGV[8] then return 0 end
end
if redis.call("ZSCORE", KEYS[3], ARGV[7]) then
  redis.call("XACK", KEYS[1], ARGV[1], ARGV[2])
  return 2
end
local dlq_id = redis.call("XADD", KEYS[2], "MAXLEN", "~", 10000, "*",
  "original_stream", KEYS[1], "original_group", ARGV[1], "original_id", ARGV[2],
  "retry_count", ARGV[3], "payload", ARGV[4], "payload_truncated", ARGV[5],
  "failed_at", ARGV[6])
redis.call("ZADD", KEYS[3], ARGV[6], ARGV[7])
redis.call("ZREMRANGEBYRANK", KEYS[3], 0, -10001)
redis.call("EXPIRE", KEYS[3], 604800)
redis.call("XACK", KEYS[1], ARGV[1], ARGV[2])
return 1`
	n, err := RDB.Eval(ctx, script, []string{stream, deadLetterStream, markerKey},
		group, id, values["retry_count"], values["payload"], values["payload_truncated"], values["failed_at"], marker, owner).Int()
	return n > 0, err
}
