package milestone

import (
	"context"
	"eigenflux_server/pkg/json"
	"errors"

	"github.com/redis/go-redis/v9"
)

const RuleInvalidationChannel = "pubsub:milestone:rules:invalidate"

type RuleInvalidationPayload struct {
	MetricKey string `json:"metric_key"`
}

func PublishRuleInvalidation(ctx context.Context, rdb *redis.Client, metricKey string) error {
	if rdb == nil {
		return ErrNilRedisClient
	}

	payload, err := json.Marshal(RuleInvalidationPayload{MetricKey: metricKey})
	if err != nil {
		return err
	}
	return rdb.Publish(ctx, RuleInvalidationChannel, payload).Err()
}

func PublishRuleInvalidations(ctx context.Context, rdb *redis.Client, metricKeys ...string) error {
	if rdb == nil {
		return ErrNilRedisClient
	}

	seen := make(map[string]struct{}, len(metricKeys))
	for _, metricKey := range metricKeys {
		if _, ok := seen[metricKey]; ok {
			continue
		}
		seen[metricKey] = struct{}{}
		if err := PublishRuleInvalidation(ctx, rdb, metricKey); err != nil {
			return err
		}
	}
	return nil
}

func SubscribeRuleInvalidation(ctx context.Context, rdb *redis.Client, handler func(metricKey string)) error {
	if rdb == nil {
		return ErrNilRedisClient
	}
	if handler == nil {
		return errors.New("rule invalidation handler is nil")
	}

	pubsub := rdb.Subscribe(ctx, RuleInvalidationChannel)
	defer pubsub.Close()

	if _, err := pubsub.Receive(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return nil
			}

			var payload RuleInvalidationPayload
			if err := json.Unmarshal([]byte(msg.Payload), &payload); err != nil {
				return err
			}
			handler(payload.MetricKey)
		}
	}
}
