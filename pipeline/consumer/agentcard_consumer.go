package consumer

import (
	"context"
	"errors"
	"strconv"
	"time"

	"eigenflux_server/pkg/agentcard"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
)

// AgentCardConsumer rebuilds agent_cards projections from rebuild events
// published by the profile / pm / api write paths. Rebuilds are idempotent
// and read only committed fact-table state, so simple mode (ack on failure)
// is fine — the cron reconciler repairs anything dropped.
type AgentCardConsumer struct {
	runner *StreamConsumer
}

const agentCardMaxRetries = 5

func NewAgentCardConsumer() *AgentCardConsumer {
	c := &AgentCardConsumer{}
	c.runner = &StreamConsumer{
		Name:         "AgentCardConsumer",
		Stream:       agentcard.StreamRebuild,
		Group:        agentcard.GroupRebuild,
		ConsumerName: "agentcard-worker-1",
		MetricsLabel: "agentcard:rebuild",
		// Workers MUST stay 1: event order remains useful for freshness and
		// volume is low. Database fences still prevent stale writes if a Redis
		// lease expires or another entry point overlaps this worker.
		Workers:                 1,
		MaxRetries:              agentCardMaxRetries,
		RetryMinIdle:            2 * time.Second,
		FatalOnGroupCreateError: true,
		Handle:                  c.handle,
	}
	return c
}

func (c *AgentCardConsumer) Start(ctx context.Context) { c.runner.Run(ctx) }

func (c *AgentCardConsumer) handle(ctx context.Context, _ string, values map[string]any) HandleResult {
	agentIDStr, ok := values["agent_id"].(string)
	if !ok {
		logger.Default().Warn("AgentCardConsumer invalid message: missing agent_id")
		return HandleFailure
	}
	agentID, err := strconv.ParseInt(agentIDStr, 10, 64)
	if err != nil {
		logger.Default().Warn("AgentCardConsumer invalid agent_id", "agentID", agentIDStr)
		return HandleFailure
	}
	reason, _ := values["reason"].(string)

	if err := agentcard.Rebuild(ctx, db.DB, mq.RDB, agentID); err != nil {
		if errors.Is(err, agentcard.ErrAgentNotFound) {
			logger.Default().Debug("AgentCardConsumer agent gone, skipping", "agentID", agentID)
			return HandleSuccess
		}
		logger.Default().Error("AgentCardConsumer rebuild failed", "agentID", agentID, "reason", reason, "err", err)
		// A transient database/Redis failure must stay pending. The snapshot
		// reconciler is a safety net, not the primary delivery guarantee.
		return HandleRetry
	}
	logger.Default().Debug("AgentCardConsumer card rebuilt", "agentID", agentID, "reason", reason)
	return HandleSuccess
}
