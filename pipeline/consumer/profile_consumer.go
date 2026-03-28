package consumer

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/stats"
	"eigenflux_server/rpc/profile/dal"
)

const (
	profileStream = "stream:profile:update"
	profileGroup  = "cg:profile:update"
	maxRetries    = 3
)

type ProfileConsumer struct {
	llmClient  *llm.Client
	maxWorkers int
}

func NewProfileConsumer(cfg *config.Config) *ProfileConsumer {
	return &ProfileConsumer{
		llmClient:  llm.NewClient(cfg),
		maxWorkers: 10, // Fixed concurrency level
	}
}

func (c *ProfileConsumer) Start(ctx context.Context) {
	slog.Info("ProfileConsumer starting", "workers", c.maxWorkers)

	if err := mq.EnsureConsumerGroup(ctx, profileStream, profileGroup); err != nil {
		slog.Error("ProfileConsumer failed to create consumer group", "err", err)
		os.Exit(1)
	}

	// Create message channel for worker pool
	type msgTask struct {
		id     string
		values map[string]interface{}
	}
	msgChan := make(chan msgTask, c.maxWorkers*2)
	var wg sync.WaitGroup

	// Start worker pool
	for i := 0; i < c.maxWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			slog.Info("ProfileConsumer worker started", "workerID", workerID)
			for task := range msgChan {
				c.processMessage(ctx, task.id, task.values)
			}
			slog.Info("ProfileConsumer worker stopped", "workerID", workerID)
		}(i)
	}

	// Main loop: fetch messages and distribute to workers
	go func() {
		for {
			select {
			case <-ctx.Done():
				slog.Info("ProfileConsumer context cancelled, closing message channel")
				close(msgChan)
				return
			default:
			}

			msgs, err := mq.Consume(ctx, profileStream, profileGroup, "profile-worker-1", 10)
			if err != nil {
				slog.Error("ProfileConsumer consume error", "err", err)
				time.Sleep(time.Second)
				continue
			}

			for _, msg := range msgs {
				task := msgTask{
					id:     msg.ID,
					values: msg.Values,
				}
				select {
				case msgChan <- task:
					// Message sent to worker
				case <-ctx.Done():
					slog.Info("ProfileConsumer context cancelled while sending message")
					close(msgChan)
					return
				}
			}
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	slog.Info("ProfileConsumer shutting down, waiting for workers to finish...")
	wg.Wait()
	slog.Info("ProfileConsumer all workers stopped")
}

func (c *ProfileConsumer) processMessage(ctx context.Context, msgID string, values map[string]interface{}) {
	agentIDStr, ok := values["agent_id"].(string)
	if !ok {
		slog.Warn("ProfileConsumer invalid message: missing agent_id")
		mq.Ack(ctx, profileStream, profileGroup, msgID)
		return
	}

	agentID, err := strconv.ParseInt(agentIDStr, 10, 64)
	if err != nil {
		slog.Warn("ProfileConsumer invalid agent_id", "agentID", agentIDStr)
		mq.Ack(ctx, profileStream, profileGroup, msgID)
		return
	}

	slog.Info("ProfileConsumer processing agent", "agentID", agentID)

	// Set status to processing (1)
	dal.UpdateAgentProfileStatus(db.DB, agentID, 1)

	// Get agent bio
	agent, err := dal.GetAgentByID(db.DB, agentID)
	if err != nil {
		slog.Warn("ProfileConsumer agent not found", "agentID", agentID, "err", err)
		dal.UpdateAgentProfileStatus(db.DB, agentID, 2) // failed
		mq.Ack(ctx, profileStream, profileGroup, msgID)
		return
	}

	if agent.Bio == "" {
		slog.Debug("ProfileConsumer agent has empty bio, skipping", "agentID", agentID)
		dal.UpdateAgentProfileStatus(db.DB, agentID, 3) // done with no keywords
		mq.Ack(ctx, profileStream, profileGroup, msgID)
		return
	}

	// Call LLM to extract keywords with retries
	var keywords []string
	var country string
	for attempt := 1; attempt <= maxRetries; attempt++ {
		keywords, country, err = c.llmClient.ExtractKeywords(ctx, agent.Bio)
		if err == nil {
			break
		}
		slog.Warn("ProfileConsumer LLM attempt failed", "attempt", attempt, "maxRetries", maxRetries, "agentID", agentID, "err", err)
		time.Sleep(time.Duration(attempt) * time.Second)
	}

	if err != nil {
		slog.Error("ProfileConsumer all retries failed", "agentID", agentID, "err", err)
		dal.UpdateAgentProfileStatus(db.DB, agentID, 2) // failed
		mq.Ack(ctx, profileStream, profileGroup, msgID)
		return
	}

	// Update keywords, country and status to done (3)
	dal.UpdateAgentProfileKeywords(db.DB, agentID, keywords, country, 3)
	slog.Info("ProfileConsumer agent keywords updated", "agentID", agentID, "keywords", keywords, "country", country)

	// Incremental sync: add country to stats set
	if country != "" {
		if err := stats.AddAgentCountry(ctx, mq.RDB, country); err != nil {
			slog.Warn("ProfileConsumer failed to sync country to stats", "err", err)
		}
	}

	mq.Ack(ctx, profileStream, profileGroup, msgID)
}
