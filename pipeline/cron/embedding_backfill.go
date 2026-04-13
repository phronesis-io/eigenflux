package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"eigenflux_server/pipeline/embedding"
	"eigenflux_server/pkg/cache"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	embcodec "eigenflux_server/pkg/embedding"
	"eigenflux_server/pkg/logger"
	profileDal "eigenflux_server/rpc/profile/dal"

	"github.com/redis/go-redis/v9"
)

const (
	lockKeyEmbBackfill          = "lock:cron:embedding_backfill"
	defaultEmbBackfillBatchSize = 200
	defaultEmbBackfillInterval  = 5 * time.Minute
	defaultEmbBackfillWorkers   = 4
	defaultEmbBackfillPause     = 100 * time.Millisecond
)

type embeddingBackfillSettings struct {
	batchSize int
	interval  time.Duration
	workers   int
	pause     time.Duration
}

type embeddingBackfillResult int

const (
	embeddingBackfillSuccess embeddingBackfillResult = iota
	embeddingBackfillFailed
	embeddingBackfillSkipped
)

func embeddingBackfillSettingsFromConfig(cfg *config.Config) embeddingBackfillSettings {
	settings := embeddingBackfillSettings{
		batchSize: defaultEmbBackfillBatchSize,
		interval:  defaultEmbBackfillInterval,
		workers:   defaultEmbBackfillWorkers,
		pause:     defaultEmbBackfillPause,
	}
	if cfg == nil {
		return settings
	}
	if cfg.EmbeddingBackfillBatchSize > 0 {
		settings.batchSize = cfg.EmbeddingBackfillBatchSize
	}
	if cfg.EmbeddingBackfillWorkers > 0 {
		settings.workers = cfg.EmbeddingBackfillWorkers
	}
	if cfg.EmbeddingBackfillPauseMs >= 0 {
		settings.pause = time.Duration(cfg.EmbeddingBackfillPauseMs) * time.Millisecond
	}
	if cfg.EmbeddingBackfillInterval != "" {
		if d, err := time.ParseDuration(cfg.EmbeddingBackfillInterval); err == nil && d > 0 {
			settings.interval = d
		}
	}
	return settings
}

func buildEmbeddingBackfillInput(bio, keywords, country string) string {
	embInput := strings.TrimSpace(bio)
	if keywords != "" {
		if embInput != "" {
			embInput += "\n"
		}
		embInput += strings.ReplaceAll(keywords, ",", ", ")
	}
	if country != "" {
		if embInput != "" {
			embInput += ". "
		}
		embInput += country
	}
	return strings.TrimSpace(embInput)
}

// StartEmbeddingBackfill runs on startup then every configured interval to generate
// embeddings for profiles that have keywords but no embedding yet.
func StartEmbeddingBackfill(ctx context.Context, cfg *config.Config, rdb *redis.Client) {
	settings := embeddingBackfillSettingsFromConfig(cfg)
	embClient := embedding.NewClient(
		cfg.EmbeddingProvider, cfg.EmbeddingApiKey, cfg.EmbeddingBaseURL,
		cfg.EmbeddingModel, cfg.EmbeddingDimensions,
	)
	embCache := cache.NewEmbeddingCache(rdb, 24*time.Hour)

	runEmbeddingBackfill(ctx, rdb, embClient, embCache, settings)

	ticker := time.NewTicker(settings.interval)
	defer ticker.Stop()

	logger.Default().Info(
		"embedding backfill started",
		"interval", settings.interval.String(),
		"batchSize", settings.batchSize,
		"workers", settings.workers,
		"pause", settings.pause.String(),
	)

	for {
		select {
		case <-ctx.Done():
			logger.Default().Info("embedding backfill stopped")
			return
		case <-ticker.C:
			runEmbeddingBackfill(ctx, rdb, embClient, embCache, settings)
		}
	}
}

func runEmbeddingBackfill(ctx context.Context, rdb *redis.Client, embClient *embedding.Client, embCache *cache.EmbeddingCache, settings embeddingBackfillSettings) {
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
		Limit(settings.batchSize).
		Find(&profiles).Error; err != nil {
		logger.Default().Error("embedding backfill query failed", "err", err)
		return
	}

	if len(profiles) == 0 {
		logger.Default().Debug("embedding backfill: nothing to do")
		return
	}

	logger.Default().Info("embedding backfill starting", "profiles", len(profiles))

	agentIDs := make([]int64, 0, len(profiles))
	for _, p := range profiles {
		agentIDs = append(agentIDs, p.AgentID)
	}

	var agents []profileDal.Agent
	if err := db.DB.Where("agent_id IN ?", agentIDs).Find(&agents).Error; err != nil {
		logger.Default().Error("embedding backfill agent preload failed", "err", err)
		return
	}

	agentByID := make(map[int64]profileDal.Agent, len(agents))
	for _, agent := range agents {
		agentByID[agent.AgentID] = agent
	}

	profileCh := make(chan profileDal.AgentProfile)
	resultCh := make(chan embeddingBackfillResult, len(profiles))

	var wg sync.WaitGroup
	for i := 0; i < settings.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for profile := range profileCh {
				resultCh <- processEmbeddingBackfillProfile(ctx, profile, agentByID, embClient, embCache, settings.pause)
			}
		}()
	}

	for _, profile := range profiles {
		if ctx.Err() != nil {
			break
		}
		profileCh <- profile
	}
	close(profileCh)

	wg.Wait()
	close(resultCh)

	success, failed, skipped := 0, 0, 0
	for result := range resultCh {
		switch result {
		case embeddingBackfillSuccess:
			success++
		case embeddingBackfillFailed:
			failed++
		default:
			skipped++
		}
	}

	logger.Default().Info("embedding backfill complete", "success", success, "failed", failed, "skipped", skipped)
}

func processEmbeddingBackfillProfile(
	ctx context.Context,
	profile profileDal.AgentProfile,
	agentByID map[int64]profileDal.Agent,
	embClient *embedding.Client,
	embCache *cache.EmbeddingCache,
	pause time.Duration,
) embeddingBackfillResult {
	if ctx.Err() != nil {
		return embeddingBackfillSkipped
	}

	agent, ok := agentByID[profile.AgentID]
	if !ok {
		logger.Default().Warn("embedding backfill: agent not found", "agentID", profile.AgentID)
		return embeddingBackfillFailed
	}

	embInput := buildEmbeddingBackfillInput(agent.Bio, profile.Keywords, profile.Country)
	if embInput == "" {
		return embeddingBackfillSkipped
	}

	emb, err := embClient.GetEmbedding(ctx, embInput)
	if pause > 0 {
		time.Sleep(pause)
	}
	if err != nil {
		logger.Default().Warn("embedding backfill: API error", "agentID", profile.AgentID, "err", err)
		return embeddingBackfillFailed
	}

	encoded := embcodec.Encode(emb)
	if err := profileDal.UpdateAgentProfileEmbedding(db.DB, profile.AgentID, encoded, embClient.Model()); err != nil {
		logger.Default().Warn("embedding backfill: DB error", "agentID", profile.AgentID, "err", err)
		return embeddingBackfillFailed
	}

	go embCache.Set(context.Background(), profile.AgentID, encoded)

	logger.Default().Debug("embedding backfill: done", "agentID", profile.AgentID, "dims", len(emb))
	return embeddingBackfillSuccess
}
