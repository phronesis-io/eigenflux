package consumer

import (
	"context"
	"strconv"
	"strings"
	"time"

	"eigenflux_server/pipeline/embedding"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/agentcard"
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
	profileStream       = "stream:profile:update"
	profileGroup        = "cg:profile:update"
	profileConsumerName = "profile-worker-1"
	profileMetricsLabel = "profile:update"
	maxRetries          = 3
)

type ProfileConsumer struct {
	llmClient       *llm.Client
	nameLLMClient   *llm.Client
	embeddingClient *embedding.Client
	profileCache    *cache.ProfileCache
	embeddingCache  *cache.EmbeddingCache
	runner          *StreamConsumer
	testMode        bool
}

func deterministicProfileMode(cfg *config.Config) bool {
	mode, err := cfg.CommissionIntegrationMode()
	return err == nil && mode.Enabled
}

func NewProfileConsumer(cfg *config.Config, prompts *llm.PromptRegistry) *ProfileConsumer {
	llmClient := llm.NewClient(cfg, prompts)
	integrationProfile := deterministicProfileMode(cfg)
	c := &ProfileConsumer{
		llmClient:       llmClient,
		nameLLMClient:   llmClient.WithModel(cfg.LLMTranslateModel).WithReasoningOff(),
		embeddingClient: embedding.NewClient(cfg.EmbeddingProvider, cfg.EmbeddingApiKey, cfg.EmbeddingBaseURL, cfg.EmbeddingModel, cfg.EmbeddingDimensions),
		profileCache:    cache.NewProfileCache(mq.RDB, time.Duration(cfg.ProfileCacheTTL)*time.Second),
		embeddingCache:  cache.NewEmbeddingCache(mq.RDB, 24*time.Hour),
		testMode:        integrationProfile,
	}
	c.runner = &StreamConsumer{
		Name:                    "ProfileConsumer",
		Stream:                  profileStream,
		Group:                   profileGroup,
		ConsumerName:            profileConsumerName,
		MetricsLabel:            profileMetricsLabel,
		Workers:                 10,
		FatalOnGroupCreateError: true,
		Handle:                  c.handle,
	}
	return c
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

func deterministicTestProfileFeatures(bio string) ([]string, string) {
	words := strings.Fields(strings.ToLower(bio))
	keywords := make([]string, 0, len(words))
	seen := make(map[string]struct{}, len(words))
	for _, word := range words {
		word = strings.Trim(word, ".,;:!?()[]{}\"'")
		if len(word) < 3 {
			continue
		}
		if _, ok := seen[word]; ok {
			continue
		}
		seen[word] = struct{}{}
		keywords = append(keywords, word)
	}
	return keywords, ""
}

func (c *ProfileConsumer) Start(ctx context.Context) { c.runner.Run(ctx) }

func (c *ProfileConsumer) handle(ctx context.Context, _ string, values map[string]any) HandleResult {
	agentIDStr, ok := values["agent_id"].(string)
	if !ok {
		logger.Default().Warn("ProfileConsumer invalid message: missing agent_id")
		return HandleFailure
	}

	agentID, err := strconv.ParseInt(agentIDStr, 10, 64)
	if err != nil {
		logger.Default().Warn("ProfileConsumer invalid agent_id", "agentID", agentIDStr)
		return HandleFailure
	}

	logger.Default().Info("ProfileConsumer processing agent", "agentID", agentID)

	// Set status to processing (1)
	dal.UpdateAgentProfileStatus(db.DB, agentID, 1)

	// Get agent bio
	agent, err := dal.GetAgentByID(db.DB, agentID)
	if err != nil {
		logger.Default().Warn("ProfileConsumer agent not found", "agentID", agentID, "err", err)
		dal.UpdateAgentProfileStatus(db.DB, agentID, 2) // failed
		return HandleFailure
	}

	if !c.testMode && agent.AgentNameEn == "" && agent.AgentName != "" {
		var englishName string
		for attempt := 1; attempt <= maxRetries; attempt++ {
			englishName, err = c.nameLLMClient.TranslateAgentNameToEnglish(ctx, agent.AgentName)
			if err == nil {
				break
			}
			logger.Default().Warn("ProfileConsumer agent-name translation failed", "attempt", attempt, "maxRetries", maxRetries, "agentID", agentID, "err", err)
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		if err != nil {
			logger.Default().Error("ProfileConsumer agent-name translation exhausted retries", "agentID", agentID, "err", err)
			return HandleRetry
		}
		if err := dal.UpdateAgentEnglishName(db.DB, agentID, agent.AgentName, englishName); err != nil {
			logger.Default().Error("ProfileConsumer failed to persist English agent name", "agentID", agentID, "err", err)
			return HandleRetry
		}
		logger.Default().Info("ProfileConsumer English agent name updated", "agentID", agentID)
	}

	if agent.Bio == "" {
		logger.Default().Debug("ProfileConsumer agent has empty bio, skipping", "agentID", agentID)
		dal.UpdateAgentProfileStatus(db.DB, agentID, 3) // done with no keywords
		return HandleSuccess
	}

	var keywords []string
	var country string
	if c.testMode {
		keywords, country = deterministicTestProfileFeatures(agent.Bio)
	} else {
		// Call LLM to extract keywords with retries
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
			return HandleFailure
		}
	}

	// Update keywords, country and status to done (3)
	dal.UpdateAgentProfileKeywords(db.DB, agentID, keywords, country, 3)
	if c.profileCache != nil {
		if err := c.profileCache.Set(ctx, buildCachedProfile(agentID, keywords, country)); err != nil {
			logger.Default().Warn("ProfileConsumer failed to refresh profile cache", "agentID", agentID, "err", err)
		}
	}
	logger.Default().Info("ProfileConsumer agent keywords updated", "agentID", agentID, "keywords", keywords, "country", country)

	// keywords project into the Card as interests_positive; refresh it now
	// that extraction finished (the bio-triggered rebuild ran before this).
	agentcard.PublishRebuild(ctx, agentID, "keywords_extracted")

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

	return HandleSuccess
}
