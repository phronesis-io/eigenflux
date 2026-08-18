package agentcard

import (
	"context"
	"strconv"

	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
)

const (
	// StreamRebuild carries "rebuild this agent's card" events. Its explicit
	// 100k cap absorbs a large burst without making the shared Redis stream
	// unbounded; the persistent full-reconcile cursor repairs any rare trim.
	StreamRebuild = "stream:agentcard:rebuild"
	// GroupRebuild is the pipeline consumer group on StreamRebuild.
	GroupRebuild = "cg:agentcard:rebuild"
)

// PublishRebuild enqueues an async card rebuild for one agent. Best-effort:
// failures are logged, never surfaced — the projection is reconciled by the
// daily full cron pass. Call after the fact-table transaction commits.
func PublishRebuild(ctx context.Context, agentID int64, reason string) {
	if mq.RDB == nil {
		logger.Default().Warn("agentcard rebuild publish skipped: redis unavailable", "agentID", agentID, "reason", reason)
		return
	}
	if _, err := mq.PublishCapped(ctx, StreamRebuild, 100000, map[string]interface{}{
		"agent_id": strconv.FormatInt(agentID, 10),
		"reason":   reason,
	}); err != nil {
		logger.Default().Warn("agentcard rebuild publish failed", "agentID", agentID, "reason", reason, "err", err)
	}
}
