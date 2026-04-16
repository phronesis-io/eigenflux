package consumer

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"eigenflux_server/pipeline/embedding"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/cache"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	embcodec "eigenflux_server/pkg/embedding"
	"eigenflux_server/pkg/logger"
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
	llmClient       *llm.Client
	embeddingClient *embedding.Client
	profileCache    *cache.ProfileCache
	embeddingCache  *cache.EmbeddingCache
	maxWorkers      int
}

func NewProfileConsumer(cfg *config.Config, prompts *llm.PromptRegistry) *ProfileConsumer {
	return &ProfileConsumer{
		llmClient:       llm.NewClient(cfg, prompts),
		embeddingClient: embedding.NewClient(cfg.EmbeddingProvider, cfg.EmbeddingApiKey, cfg.EmbeddingBaseURL, cfg.EmbeddingModel, cfg.EmbeddingDimensions),
		profileCache:    cache.NewProfileCache(mq.RDB, time.Duration(cfg.ProfileCacheTTL)*time.Second),
		embeddingCache:  cache.NewEmbeddingCache(mq.RDB, 24*time.Hour),
		maxWorkers:      10, // Fixed concurrency level
	}
}

func buildCachedProfile(agentID int64, keywords []string, country string) *cache.CachedProfile {
	return &cache.CachedProfile{
		AgentID:    agentID,
		Keywords:   keywords,
		Domains:    keywords,
		Geo:        "",
		GeoCountry: country,
	}
}

func (c *ProfileConsumer) Start(ctx context.Context) {
	logger.Default().Info("ProfileConsumer starting", "workers", c.maxWorkers)

	if err := mq.EnsureConsumerGroup(ctx, profileStream, profileGroup); err != nil {
		logger.Default().Error("ProfileConsumer failed to create consumer group", "err", err)
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
			logger.Default().Info("ProfileConsumer worker started", "workerID", workerID)
			for task := range msgChan {
				c.processMessage(ctx, task.id, task.values)
			}
			logger.Default().Info("ProfileConsumer worker stopped", "workerID", workerID)
		}(i)
	}

	// Main loop: fetch messages and distribute to workers
	go func() {
		for {
			select {
			case <-ctx.Done():
				logger.Default().Info("ProfileConsumer context cancelled, closing message channel")
				close(msgChan)
				return
			default:
			}

			msgs, err := mq.Consume(ctx, profileStream, profileGroup, "profile-worker-1", 10)
			if err != nil {
				logger.Default().Error("ProfileConsumer consume error", "err", err)
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
					logger.Default().Info("ProfileConsumer context cancelled while sending message")
					close(msgChan)
					return
				}
			}
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	logger.Default().Info("ProfileConsumer shutting down, waiting for workers to finish...")
	wg.Wait()
	logger.Default().Info("ProfileConsumer all workers stopped")
}

func (c *ProfileConsumer) processMessage(ctx context.Context, msgID string, values map[string]interface{}) {
	agentIDStr, ok := values["agent_id"].(string)
	if !ok {
		logger.Default().Warn("ProfileConsumer invalid message: missing agent_id")
		mq.Ack(ctx, profileStream, profileGroup, msgID)
		return
	}

	agentID, err := strconv.ParseInt(agentIDStr, 10, 64)
	if err != nil {
		logger.Default().Warn("ProfileConsumer invalid agent_id", "agentID", agentIDStr)
		mq.Ack(ctx, profileStream, profileGroup, msgID)
		return
	}

	logger.Default().Info("ProfileConsumer processing agent", "agentID", agentID)

	// Set status to processing (1)
	dal.UpdateAgentProfileStatus(db.DB, agentID, 1)

	// Get agent bio
	agent, err := dal.GetAgentByID(db.DB, agentID)
	if err != nil {
		logger.Default().Warn("ProfileConsumer agent not found", "agentID", agentID, "err", err)
		dal.UpdateAgentProfileStatus(db.DB, agentID, 2) // failed
		mq.Ack(ctx, profileStream, profileGroup, msgID)
		return
	}

	if agent.Bio == "" {
		logger.Default().Debug("ProfileConsumer agent has empty bio, skipping", "agentID", agentID)
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
		logger.Default().Warn("ProfileConsumer LLM attempt failed", "attempt", attempt, "maxRetries", maxRetries, "agentID", agentID, "err", err)
		time.Sleep(time.Duration(attempt) * time.Second)
	}

	if err != nil {
		logger.Default().Error("ProfileConsumer all retries failed", "agentID", agentID, "err", err)
		dal.UpdateAgentProfileStatus(db.DB, agentID, 2) // failed
		mq.Ack(ctx, profileStream, profileGroup, msgID)
		return
	}

	// Update keywords, country and status to done (3)
	dal.UpdateAgentProfileKeywords(db.DB, agentID, keywords, country, 3)
	if c.profileCache != nil {
		if err := c.profileCache.Set(ctx, buildCachedProfile(agentID, keywords, country)); err != nil {
			logger.Default().Warn("ProfileConsumer failed to refresh profile cache", "agentID", agentID, "err", err)
		}
	}
	logger.Default().Info("ProfileConsumer agent keywords updated", "agentID", agentID, "keywords", keywords, "country", country)

	// Generate profile embedding from bio + keywords + country (fire-and-forget)
	go func() {
		embInput := agent.Bio
		if len(keywords) > 0 {
			embInput += "\n" + strings.Join(keywords, ", ")
		}
		if country != "" {
			embInput += ". " + country
		}

		emb, err := c.embeddingClient.GetEmbedding(context.Background(), embInput)
		if err != nil {
			logger.Default().Warn("ProfileConsumer failed to generate embedding", "agentID", agentID, "err", err)
			return
		}

		encoded := embcodec.Encode(emb)
		if err := dal.UpdateAgentProfileEmbedding(db.DB, agentID, encoded, c.embeddingClient.Model()); err != nil {
			logger.Default().Warn("ProfileConsumer failed to save embedding", "agentID", agentID, "err", err)
		} else {
			if c.embeddingCache != nil {
				if err := c.embeddingCache.Set(context.Background(), agentID, encoded); err != nil {
					logger.Default().Warn("ProfileConsumer failed to refresh embedding cache", "agentID", agentID, "err", err)
				}
			}
			logger.Default().Info("ProfileConsumer profile embedding updated", "agentID", agentID, "dims", len(emb))
		}
	}()

	// Incremental sync: add country to stats set
	if country != "" {
		if err := stats.AddAgentCountry(ctx, mq.RDB, country); err != nil {
			logger.Default().Warn("ProfileConsumer failed to sync country to stats", "err", err)
		}
	}

	mq.Ack(ctx, profileStream, profileGroup, msgID)
}
