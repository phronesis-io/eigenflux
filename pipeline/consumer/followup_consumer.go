package consumer

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/followuplog"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/logger"
)

const (
	followupBatchSize     = int64(100)
	followupMaxRetryCount = int64(3)
	followupRetryMinIdle  = time.Second
	followupPollInterval  = 200 * time.Millisecond
	followupReadBlock     = 500 * time.Millisecond
	followupMaxWorkers    = 5
)

type FollowupConsumer struct {
	idGen        *idgen.ManagedGenerator
	consumerName string
}

func NewFollowupConsumer(idGen *idgen.ManagedGenerator) *FollowupConsumer {
	hostname, _ := os.Hostname()
	name := fmt.Sprintf("followup-worker-%s-%d", hostname, os.Getpid())
	return &FollowupConsumer{idGen: idGen, consumerName: name}
}

func (c *FollowupConsumer) Start(ctx context.Context) {
	runner := &StreamConsumer{
		Name:                    "FollowupConsumer",
		Stream:                  followuplog.StreamName,
		Group:                   followuplog.GroupName,
		ConsumerName:            c.consumerName,
		MetricsLabel:            "followup:label",
		Workers:                 followupMaxWorkers,
		BatchSize:               followupBatchSize,
		MaxRetries:              followupMaxRetryCount,
		RetryMinIdle:            followupRetryMinIdle,
		PollInterval:            followupPollInterval,
		ReadBlock:               followupReadBlock,
		FatalOnGroupCreateError: false,
		Handle:                  c.handle,
	}
	runner.Run(ctx)
}

func (c *FollowupConsumer) handle(_ context.Context, msgID string, values map[string]any) HandleResult {
	agentIDStr, _ := values["agent_id"].(string)
	agentID, err := strconv.ParseInt(agentIDStr, 10, 64)
	if err != nil {
		logger.Default().Warn("FollowupConsumer invalid agent_id", "msgID", msgID, "raw", agentIDStr, "err", err)
		return HandleFailure
	}
	itemIDStr, _ := values["item_id"].(string)
	itemID, err := strconv.ParseInt(itemIDStr, 10, 64)
	if err != nil {
		logger.Default().Warn("FollowupConsumer invalid item_id", "msgID", msgID, "raw", itemIDStr, "err", err)
		return HandleFailure
	}
	kind, _ := values["kind"].(string)
	dedupKey, _ := values["dedup_key"].(string)
	if kind == "" || dedupKey == "" {
		logger.Default().Warn("FollowupConsumer missing kind/dedup_key", "msgID", msgID)
		return HandleFailure
	}
	reportedAtStr, _ := values["reported_at"].(string)
	reportedAt, _ := strconv.ParseInt(reportedAtStr, 10, 64)

	impressionID, _ := values["impression_id"].(string)
	brief, _ := values["brief"].(string)
	sessionKey, _ := values["session_key"].(string)
	channel, _ := values["channel"].(string)
	serverID, _ := values["server_id"].(string)

	rowID, err := c.idGen.NextID()
	if err != nil {
		logger.Default().Error("FollowupConsumer failed to generate row id", "err", err)
		return HandleRetry
	}

	row := FollowupLabel{
		ID:           rowID,
		AgentID:      agentID,
		ItemID:       itemID,
		Kind:         kind,
		ImpressionID: impressionID,
		Brief:        brief,
		SessionKey:   sessionKey,
		Channel:      channel,
		ServerID:     serverID,
		DedupKey:     dedupKey,
		ReportedAt:   reportedAt,
		CreatedAt:    nowMs(),
	}

	if err := batchInsertFollowupLabels(db.DB, []FollowupLabel{row}); err != nil {
		logger.Default().Error("FollowupConsumer insert failed", "err", err)
		return HandleRetry
	}
	return HandleSuccess
}
