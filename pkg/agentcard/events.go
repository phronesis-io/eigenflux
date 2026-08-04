package agentcard

import (
	"context"
	"strconv"

	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
)

const (
	// StreamRebuild carries "rebuild this agent's card" events. Capped stream
	// (default MAXLEN) is fine: rebuilds are idempotent and the cron
	// reconciler rebuilds every card at least once per day anyway.
	StreamRebuild = "stream:agentcard:rebuild"
	// GroupRebuild is the pipeline consumer group on StreamRebuild.
	GroupRebuild = "cg:agentcard:rebuild"
)

// PublishRebuild enqueues an async card rebuild for one agent. Best-effort:
// failures are logged, never surfaced — the projection is reconciled by the
// daily full cron pass. Call after the fact-table transaction commits.
func PublishRebuild(ctx context.Context, agentID int64, reason string) {
	if _, err := mq.Publish(ctx, StreamRebuild, map[string]interface{}{
		"agent_id": strconv.FormatInt(agentID, 10),
		"reason":   reason,
	}); err != nil {
		logger.Default().Warn("agentcard rebuild publish failed", "agentID", agentID, "reason", reason, "err", err)
	}
}
