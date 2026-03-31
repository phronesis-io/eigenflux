package mq

import (
	"context"
	"log/slog"
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
		slog.Error("failed to connect to redis", "err", err)
		os.Exit(1)
	}
}

// Publish sends a message to a Redis Stream
func Publish(ctx context.Context, stream string, values map[string]interface{}) (string, error) {
	return RDB.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		Values: values,
	}).Result()
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
		ids = append(ids, entry.ID)
		metaByID[entry.ID] = entry
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
