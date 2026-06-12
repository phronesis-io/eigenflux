package consumer

import (
	"context"
	"eigenflux_server/pkg/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/replaylog"
)

const (
	replayBatchSize         = int64(100)
	replayMaxRetryCount     = int64(3)
	replayRetryMinIdle      = time.Second
	replayRetryPollInterval = 200 * time.Millisecond
	replayReadBlock         = 500 * time.Millisecond
	replayMaxWorkers        = 5
)

type ReplayConsumer struct {
	idGen        *idgen.ManagedGenerator
	consumerName string
}

func NewReplayConsumer(idGen *idgen.ManagedGenerator) *ReplayConsumer {
	hostname, _ := os.Hostname()
	name := fmt.Sprintf("replay-worker-%s-%d", hostname, os.Getpid())
	return &ReplayConsumer{idGen: idGen, consumerName: name}
}

func (c *ReplayConsumer) Start(ctx context.Context) {
	runner := &StreamConsumer{
		Name:                    "ReplayConsumer",
		Stream:                  replaylog.StreamName,
		Group:                   replaylog.GroupName,
		ConsumerName:            c.consumerName,
		MetricsLabel:            "replay:log",
		Workers:                 replayMaxWorkers,
		BatchSize:               replayBatchSize,
		MaxRetries:              replayMaxRetryCount,
		RetryMinIdle:            replayRetryMinIdle,
		PollInterval:            replayRetryPollInterval,
		ReadBlock:               replayReadBlock,
		FatalOnGroupCreateError: false, // log-and-return; matches prior behavior
		Handle:                  c.handle,
	}
	runner.Run(ctx)
}

func (c *ReplayConsumer) handle(_ context.Context, msgID string, values map[string]any) HandleResult {
	agentIDStr, _ := values["agent_id"].(string)
	agentID, err := strconv.ParseInt(agentIDStr, 10, 64)
	if err != nil {
		logger.Default().Warn("ReplayConsumer invalid agent_id", "raw", agentIDStr, "err", err)
		return HandleFailure
	}

	impressionID, _ := values["impression_id"].(string)
	if impressionID == "" {
		logger.Default().Warn("ReplayConsumer invalid impression_id", "msgID", msgID)
		return HandleFailure
	}

	agentFeatures, _ := values["agent_features"].(string)
	if agentFeatures == "" {
		agentFeatures = "{}"
	}

	servedAtStr, _ := values["served_at"].(string)
	servedAt, _ := strconv.ParseInt(servedAtStr, 10, 64)

	// delivered is absent on events from pre-upgrade feed binaries (rolling
	// deploy); leave it NULL so those rows never count as delivered.
	var delivered *bool
	if deliveredStr, ok := values["delivered"].(string); ok && deliveredStr != "" {
		flag := deliveredStr == "1"
		delivered = &flag
	}

	itemsStr, _ := values["items"].(string)
	var servedItems []replaylog.ServedItem
	if err := json.Unmarshal([]byte(itemsStr), &servedItems); err != nil {
		logger.Default().Warn("ReplayConsumer invalid items JSON", "err", err)
		return HandleFailure
	}

	if len(servedItems) == 0 {
		return HandleSuccess
	}

	now := nowMs()
	logs := make([]ReplayLog, 0, len(servedItems))
	for _, si := range servedItems {
		rowID, err := c.idGen.NextID()
		if err != nil {
			logger.Default().Error("ReplayConsumer failed to generate row id", "err", err)
			return HandleRetry
		}

		itemFeatures := si.ItemFeatures
		if itemFeatures == "" {
			itemFeatures = "{}"
		}

		score := si.Score
		logs = append(logs, ReplayLog{
			ID:            rowID,
			ImpressionID:  impressionID,
			AgentID:       agentID,
			ItemID:        si.ItemID,
			AgentFeatures: agentFeatures,
			ItemFeatures:  itemFeatures,
			ItemScore:     &score,
			Position:      si.Position,
			ServedAt:      servedAt,
			CreatedAt:     now,
			Delivered:     delivered,
		})
	}

	if err := batchInsertReplayLogs(db.DB, logs); err != nil {
		logger.Default().Error("ReplayConsumer failed to insert replay logs", "err", err, "count", len(logs))
		return HandleRetry
	}

	logger.Default().Info("ReplayConsumer inserted replay logs", "impressionID", impressionID, "count", len(logs))
	return HandleSuccess
}
