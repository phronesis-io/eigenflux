package main

import (
	"context"
	"strings"
	"time"

	"eigenflux_server/pkg/cache"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	embcodec "eigenflux_server/pkg/embedding"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pipeline/embedding"
	profileDal "eigenflux_server/rpc/profile/dal"

	"github.com/redis/go-redis/v9"
)

const (
	lockKeyEmbBackfill   = "lock:cron:embedding_backfill"
	embBackfillBatchSize = 50
	embBackfillPause     = 200 * time.Millisecond // per-profile pause to avoid API burst
)

// StartEmbeddingBackfill runs on startup then every 30 minutes to generate
// embeddings for profiles that have keywords but no embedding yet.
func StartEmbeddingBackfill(ctx context.Context, cfg *config.Config, rdb *redis.Client) {
	embClient := embedding.NewClient(
		cfg.EmbeddingProvider, cfg.EmbeddingApiKey, cfg.EmbeddingBaseURL,
		cfg.EmbeddingModel, cfg.EmbeddingDimensions,
	)
	embCache := cache.NewEmbeddingCache(rdb, 24*time.Hour)

	runEmbeddingBackfill(ctx, rdb, embClient, embCache)

	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	logger.Default().Info("embedding backfill started", "interval", "30m")

	for {
		select {
		case <-ctx.Done():
			logger.Default().Info("embedding backfill stopped")
			return
		case <-ticker.C:
			runEmbeddingBackfill(ctx, rdb, embClient, embCache)
		}
	}
}

func runEmbeddingBackfill(ctx context.Context, rdb *redis.Client, embClient *embedding.Client, embCache *cache.EmbeddingCache) {
	acquired, err := acquireLock(ctx, rdb, lockKeyEmbBackfill, 25*time.Minute)
	if err != nil {
		logger.Default().Warn("embedding backfill lock error", "err", err)
		return
	}
	if !acquired {
		logger.Default().Debug("embedding backfill skipped (another instance running)")
		return
	}
	defer releaseLock(ctx, rdb, lockKeyEmbBackfill)

	// Query profiles: status=3 (done), has keywords, missing embedding
	var profiles []profileDal.AgentProfile
	if err := db.DB.
		Where("status = 3 AND keywords != '' AND (profile_embedding IS NULL OR length(profile_embedding) = 0)").
		Limit(embBackfillBatchSize).
		Find(&profiles).Error; err != nil {
		logger.Default().Error("embedding backfill query failed", "err", err)
		return
	}

	if len(profiles) == 0 {
		logger.Default().Debug("embedding backfill: nothing to do")
		return
	}

	logger.Default().Info("embedding backfill starting", "profiles", len(profiles))

	success, failed := 0, 0
	for _, p := range profiles {
		if ctx.Err() != nil {
			break
		}

		// Fetch agent bio
		var agent profileDal.Agent
		if err := db.DB.Where("agent_id = ?", p.AgentID).First(&agent).Error; err != nil {
			logger.Default().Warn("embedding backfill: agent not found", "agentID", p.AgentID, "err", err)
			failed++
			continue
		}

		// Build embedding input (same logic as profile_consumer.go)
		embInput := agent.Bio
		if p.Keywords != "" {
			embInput += "\n" + strings.ReplaceAll(p.Keywords, ",", ", ")
		}
		if p.Country != "" {
			embInput += ". " + p.Country
		}
		if strings.TrimSpace(embInput) == "" {
			continue
		}

		emb, err := embClient.GetEmbedding(ctx, embInput)
		if err != nil {
			logger.Default().Warn("embedding backfill: API error", "agentID", p.AgentID, "err", err)
			failed++
			continue
		}

		encoded := embcodec.Encode(emb)
		if err := profileDal.UpdateAgentProfileEmbedding(db.DB, p.AgentID, encoded, embClient.Model()); err != nil {
			logger.Default().Warn("embedding backfill: DB error", "agentID", p.AgentID, "err", err)
			failed++
			continue
		}

		// Warm embedding cache
		go embCache.Set(context.Background(), p.AgentID, encoded)

		success++
		logger.Default().Debug("embedding backfill: done", "agentID", p.AgentID, "dims", len(emb))
		time.Sleep(embBackfillPause)
	}

	logger.Default().Info("embedding backfill complete", "success", success, "failed", failed, "remaining", len(profiles)-success-failed)
}
