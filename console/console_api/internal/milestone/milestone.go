package milestone

import (
	"context"
	"encoding/json"

	"github.com/redis/go-redis/v9"
)

const (
	MetricConsumed = "consumed"
	MetricScore1   = "score_1"
	MetricScore2   = "score_2"

	RuleInvalidationChannel = "pubsub:milestone:rules:invalidate"
)

func IsValidMetricKey(metricKey string) bool {
	switch metricKey {
	case MetricConsumed, MetricScore1, MetricScore2:
		return true
	default:
		return false
	}
}

type ruleInvalidationPayload struct {
	MetricKey string `json:"metric_key"`
}

func PublishRuleInvalidations(ctx context.Context, rdb *redis.Client, metricKeys ...string) error {
	if rdb == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(metricKeys))
	for _, mk := range metricKeys {
		if _, ok := seen[mk]; ok {
			continue
		}
		seen[mk] = struct{}{}
		payload, err := json.Marshal(ruleInvalidationPayload{MetricKey: mk})
		if err != nil {
			return err
		}
		if err := rdb.Publish(ctx, RuleInvalidationChannel, payload).Err(); err != nil {
			return err
		}
	}
	return nil
}
