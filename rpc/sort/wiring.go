package main

import (
	"context"
	"eigenflux_server/pipeline/embedding"
	"eigenflux_server/pkg/cache"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/featureindex"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/need"
	"eigenflux_server/pkg/recall"
	"eigenflux_server/pkg/recallsource"
	"eigenflux_server/rpc/sort/discovery"
	"eigenflux_server/rpc/sort/discovery/needembedding"
	"eigenflux_server/rpc/sort/discoverylr"

	"fmt"
	"os"
	"strings"
	"time"
)

func initDiscovery(ctx context.Context, cfg *config.Config, policies func(context.Context, []discovery.Candidate, discovery.Mode, int) ([]discovery.Candidate, error), sourceLimits []discovery.SourceLimit, needMix *discovery.NeedMix, pgcAuthors discovery.PGCAuthors) (*discovery.Service, func(), error) {
	if !cfg.EnableNeedSearch {
		return nil, func() {}, nil
	}
	if cfg.DiscoveryNeedBudgetBPS < 0 || cfg.DiscoveryNeedBudgetBPS > 10000 {
		return nil, nil, fmt.Errorf("DISCOVERY_NEED_BUDGET_BPS must be in [0,10000]")
	}
	if cfg.DiscoveryConsumedRefillBPS < 0 || cfg.DiscoveryConsumedRefillBPS > 10000 {
		return nil, nil, fmt.Errorf("DISCOVERY_CONSUMED_REFILL_BPS must be in [0,10000]")
	}
	if cfg.DiscoveryProfileSemanticBPS < 0 || cfg.DiscoveryProfileSemanticBPS > 10000 {
		return nil, nil, fmt.Errorf("DISCOVERY_PROFILE_SEMANTIC_BPS must be in [0,10000]")
	}
	if cfg.DiscoveryRatingLRBPS < 0 || cfg.DiscoveryRatingLRBPS > 10000 {
		return nil, nil, fmt.Errorf("DISCOVERY_RATING_LR_BPS must be in [0,10000]")
	}
	if cfg.DiscoveryRatingLRV2BPS < 0 || cfg.DiscoveryRatingLRV2BPS > 10000 {
		return nil, nil, fmt.Errorf("DISCOVERY_RATING_LR_V2_BPS must be in [0,10000]")
	}
	if err := cfg.ValidateCommissionDiscoveryConfiguration(); err != nil {
		return nil, nil, err
	}
	raw, err := os.ReadFile(cfg.DiscoveryRulesPath)
	if err != nil {
		return nil, nil, err
	}
	rules, err := discovery.Decode[discovery.Rules](raw)
	if err != nil {
		return nil, nil, err
	}
	if err = rules.Validate(); err != nil {
		return nil, nil, err
	}
	embed := embedding.NewClient(cfg.EmbeddingProvider, cfg.EmbeddingApiKey, cfg.EmbeddingBaseURL, cfg.EmbeddingModel, cfg.EmbeddingDimensions)
	if err = featureindex.EnsureAgentSearchIndex(ctx, cfg.AgentDiscoveryIndex, cfg.EmbeddingDimensions); err != nil {
		return nil, nil, err
	}
	ids, err := idgen.NewManagedGenerator(ctx, idgen.ManagedGeneratorConfig{Endpoints: strings.Split(cfg.EtcdAddr, ","), WorkerPrefix: cfg.IDWorkerPrefix, ServiceName: "discovery-context-id", InstanceID: cfg.IDInstanceID, LeaseTTLSecond: cfg.IDWorkerLeaseTTL, EpochMS: cfg.IDSnowflakeEpoch})
	if err != nil {
		return nil, nil, err
	}
	close := func() { _ = ids.Close(context.Background()) }
	index := cfg.CommissionIndexAlias
	if index == "" {
		index = cfg.CommissionIndexName
	}
	if err := es.EnsureBroadcastRetrievalFields(ctx, es.ReadIndexPattern); err != nil {
		close()
		return nil, nil, err
	}
	if err := es.EnsureRetrievalSlots(ctx, index, cfg.CommissionIndexName); err != nil {
		close()
		return nil, nil, err
	}
	contexts := &cache.DiscoveryCache{Redis: mq.RDB}
	vectors := needembedding.New(cfg, db.DB, mq.RDB)
	reader := recall.NewRedisRecallReader(mq.RDB, cfg.RecallRedisNamespace)
	sources := &discovery.Source{
		ManagedIntegrations: cfg.EnableSDKIntegrations,
		ContextCache:        contexts, DB: db.DB, Redis: mq.RDB,
		CommissionIndex: index, AgentIndex: cfg.AgentDiscoveryIndex,
		RecallNamespace: cfg.RecallRedisNamespace, BlockedAuthorEmails: cfg.BlockedAgentEmails,
		DisableDedup:         cfg.ShouldDisableDedup(),
		FriendFeedMaxAuthors: cfg.FriendFeedMaxAuthors, FriendFeedWindowHours: cfg.FriendFeedWindowHours, FriendFeedMaxItems: cfg.FriendFeedMaxItems,
		SwingRecall: recallsource.NewSwingI2IRecallSource(reader, recall.NewSurfaceHistoryStore(mq.RDB, cfg.RecallRedisNamespace), mq.RDB, cfg.SwingI2IRecallSeeds, cfg.SwingI2IRecallK),
		DisabledChannels: map[string]bool{
			"friend": !cfg.FriendFeedEnabled, "swing_i2i": !cfg.EnableSwingI2IRecall,
			"hot_recall": !cfg.EnableHotRecall, "new_recall": !cfg.EnableNewRecall, "new_ugc_recall": !cfg.EnableNewUGCRecall,
		},
	}
	engine := &discovery.Engine{
		FriendFeedEnabled: cfg.FriendFeedEnabled, SourceLimits: sourceLimits,
		NeedBudgetBPS: cfg.DiscoveryNeedBudgetBPS, NeedMix: needMix,
		ConsumedRefillBPS: cfg.DiscoveryConsumedRefillBPS, ProfileSemanticBPS: cfg.DiscoveryProfileSemanticBPS,
		Compiler: &discovery.Compiler{Cache: contexts, Embedder: embed, NeedVectors: vectors, EmbeddingVersion: vectors.Generation()},
		Needs:    discovery.CachedNeeds{NeedReader: need.Store{DB: db.DB}, Cache: contexts},
		IDs:      ids, Rules: rules, Policies: policies, Sources: sources,
		RatingLRBPS: cfg.DiscoveryRatingLRBPS, RatingNextBPS: cfg.DiscoveryRatingLRV2BPS, PGCAuthors: pgcAuthors,
	}
	interval, err := time.ParseDuration(cfg.DiscoveryLRReloadInterval)
	if err != nil && (cfg.DiscoveryLREnabled || cfg.DiscoveryRatingLRBPS > 0 || cfg.DiscoveryRatingLRV2BPS > 0) {
		close()
		return nil, nil, err
	}
	learned := discoverylr.New(cfg.DiscoveryLREnabled, cfg.DiscoveryLRModelPath, interval)
	rating := discoverylr.NewRating(cfg.DiscoveryRatingLRBPS > 0, cfg.DiscoveryRatingLRModelPath, interval)
	next := discoverylr.NewRatingV2(cfg.DiscoveryRatingLRBPS > 0 && cfg.DiscoveryRatingLRV2BPS > 0, cfg.DiscoveryRatingLRV2ModelPath, interval)
	engine.Learned, engine.RatingLearned, engine.RatingNext = learned, rating, next
	return &discovery.Service{Engine: engine}, func() { next.Close(); rating.Close(); learned.Close(); close() }, nil
}
