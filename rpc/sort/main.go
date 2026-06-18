package main

import (
	"context"
	"log"
	"net"
	"time"

	etcd "github.com/kitex-contrib/registry-etcd"

	"eigenflux_server/kitex_gen/eigenflux/sort/sortservice"
	"eigenflux_server/pipeline/embedding"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/bloomfilter"
	"eigenflux_server/pkg/cache"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/recall"
	"eigenflux_server/pkg/recallsource"
	"eigenflux_server/pkg/rpcx"
	"eigenflux_server/pkg/telemetry"
	"eigenflux_server/rpc/sort/ranker"
	"eigenflux_server/rpc/sort/serviceranker"
)

var bf *bloomfilter.BloomFilter
var cfg *config.Config
var searchCache *cache.SearchCache
var profileCache *cache.ProfileCache
var rankerInstance *ranker.Ranker
var rankerCfg *ranker.RankerConfig
var itemRerankPolicies *rerankPolicySet
var serviceRankerCfg *serviceranker.ServiceRankerConfig
var embeddingCache *cache.EmbeddingCache
var recallSources []recallsource.RecallSource

// SearchServices dependencies. Embeds sub-intent query text for kNN
// recall and decomposes raw queries into sub-intents when the agent omits them.
// chatClient may be nil if the prompt registry cannot be loaded; DecomposeTask
// uses an inline prompt so the chat path still works.
var embeddingClient *embedding.Client
var chatClient llm.Chat

func main() {
	cfg = config.Load()
	logFlush := logger.Init("SortService", cfg.EffectiveLokiURL(), cfg.LogLevel)
	defer logFlush()

	shutdown, err := telemetry.Init("SortService", cfg.OtelExporterEndpoint, cfg.MonitorEnabled)
	if err != nil {
		log.Fatalf("failed to init telemetry: %v", err)
	}
	defer shutdown(context.Background())

	go metrics.StartMetricsServer(cfg.SortRPCPort + 1000)

	// Initialize PostgreSQL (for fetching user profiles)
	db.Init(cfg.PgDSN)

	// Initialize Redis (for caching and bloom filter)
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)

	// Initialize Bloom Filter (for group_id deduplication)
	bf = bloomfilter.NewBloomFilter(mq.RDB)

	// Initialize cache
	if cfg.EnableSearchCache {
		searchCache = cache.NewSearchCache(
			mq.RDB,
			time.Duration(cfg.SearchCacheTTL)*time.Second,
			time.Duration(cfg.SearchCacheTTL)*time.Second,
		)
		profileCache = cache.NewProfileCache(
			mq.RDB,
			time.Duration(cfg.ProfileCacheTTL)*time.Second,
		)
		logger.Default().Info("cache enabled", "searchTTL", cfg.SearchCacheTTL, "profileTTL", cfg.ProfileCacheTTL)
	}

	// Initialize ranker
	rankerCfg = ranker.NewRankerConfig(cfg)
	rankerInstance = ranker.New(rankerCfg)
	itemRerankPolicies = loadRerankPolicySet(context.Background(), "configs/sort/rerank.yaml", time.Now)

	// Initialize service ranker (used by SearchServices for the trading domain).
	serviceRankerCfg = &serviceranker.ServiceRankerConfig{
		SemanticWeight: cfg.TradeSearchSemanticWeight,
		KeywordWeight:  cfg.TradeSearchKeywordWeight,
		SuccessWeight:  cfg.TradeSearchSuccessWeight,
		LatencyWeight:  cfg.TradeSearchLatencyWeight,
		PriceWeight:    cfg.TradeSearchPriceWeight,
		DeadlineWeight: cfg.TradeSearchDeadlineWeight,
		MaxLatencyMs:   86400000,
		MaxPriceAtomic: 1000000000,
		MaxDeadlineMs:  604800000,
	}

	// Initialize embedding cache
	embeddingCache = cache.NewEmbeddingCache(mq.RDB, 24*time.Hour)

	// Initialize embedding client (used by SearchServices to embed
	// per-sub-intent query text for kNN recall against usage_embedding).
	embeddingClient = embedding.NewClient(cfg.EmbeddingProvider, cfg.EmbeddingApiKey, cfg.EmbeddingBaseURL, cfg.EmbeddingModel, cfg.EmbeddingDimensions)

	// Initialize LLM chat client (used by SearchServices to decompose
	// raw queries into sub-intents when the agent did not provide them).
	// DecomposeTask uses an inline prompt, so a missing prompt registry is
	// not fatal — degrade chat to nil and fall back to single-intent.
	prompts, promptErr := llm.LoadDefaultPrompts()
	if promptErr != nil {
		logger.Default().Warn("sort: prompt registry unavailable; decompose chat degraded", "err", promptErr)
		prompts = nil
	}
	decomposeLLM := llm.NewClient(cfg, prompts)
	if cfg.LLMTaskDecomposeModel != "" {
		decomposeLLM = decomposeLLM.WithModel(cfg.LLMTaskDecomposeModel).WithReasoningOff()
		logger.Default().Info("sort: task decomposition model configured", "model", cfg.LLMTaskDecomposeModel)
	}
	chatClient = decomposeLLM.AsChat()

	// Initialize recall sources
	recallReader := recall.NewRedisRecallReader(mq.RDB, cfg.RecallRedisNamespace)
	if cfg.EnableHotRecall {
		recallSources = append(recallSources, recallsource.NewRedisRecallSource(recallReader, "hot_recall", recallsource.HotRecall, "hot_recall"))
	}
	if cfg.EnableNewRecall {
		recallSources = append(recallSources, recallsource.NewRedisRecallSource(recallReader, "new_recall", recallsource.NewRecall, "new_recall"))
	}
	if cfg.EnableTwoTowerRecall {
		recallSources = append(recallSources, recallsource.NewTwoTowerRecallSource(recallReader, cfg.TwoTowerRecallRedisKey, cfg.TwoTowerRecallK))
	}
	logger.Default().Info("recall sources initialized", "count", len(recallSources))

	// Initialize Elasticsearch
	if err := es.InitES(cfg.EmbeddingDimensions); err != nil {
		log.Fatalf("failed to initialize ES: %v", err)
	}

	r, err := etcd.NewEtcdRegistry([]string{cfg.EtcdAddr})
	if err != nil {
		log.Fatalf("failed to create etcd registry: %v", err)
	}

	listenAddr := cfg.ListenAddr(cfg.SortRPCPort)
	addr, _ := net.ResolveTCPAddr("tcp", listenAddr)
	svr := sortservice.NewServer(
		new(SortServiceESImpl),
		rpcx.ServerOptions(addr, r, "SortService", metrics.KitexServerMW())...,
	)

	logger.Default().Info("sort service started", "addr", listenAddr)
	if err := svr.Run(); err != nil {
		log.Fatalf("sort service failed: %v", err)
	}
}
