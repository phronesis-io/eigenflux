package main

import (
	"context"
	"log"
	"net"
	"time"

	etcd "github.com/kitex-contrib/registry-etcd"

	"eigenflux_server/kitex_gen/eigenflux/sort/sortservice"
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
	"eigenflux_server/rpc/sort/lrranker"
	"eigenflux_server/rpc/sort/ranker"
)

var bf *bloomfilter.BloomFilter
var cfg *config.Config
var searchCache *cache.SearchCache
var profileCache *cache.ProfileCache
var rankerInstance *ranker.Ranker
var rankerCfg *ranker.RankerConfig
var lrManager *lrranker.Manager
var itemRerankPolicies *rerankPolicySet
var embeddingCache *cache.EmbeddingCache
var recallSources []recallsource.RecallSource

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
	mq.SetDefaultStreamMaxLen(cfg.MqStreamMaxLen)

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

	// Initialize the LR ranker. When enabled and a valid model is present, it
	// replaces the formula ordering of eligible items with the model's
	// follow-up probability; otherwise sort transparently falls back to the
	// formula ranker. The bundle is delivered to a local directory out-of-band.
	lrReload, err := time.ParseDuration(cfg.LRRankerReloadInterval)
	if err != nil {
		lrReload = 60 * time.Second
	}
	lrManager = lrranker.NewManager(lrranker.Config{
		Enabled:        cfg.LRRankerEnabled,
		ModelPath:      cfg.LRRankerModelPath,
		ReloadInterval: lrReload,
	})
	defer lrManager.Close()

	// Initialize embedding cache
	embeddingCache = cache.NewEmbeddingCache(mq.RDB, 24*time.Hour)

	// Initialize recall sources
	recallReader := recall.NewRedisRecallReader(mq.RDB, cfg.RecallRedisNamespace)
	if cfg.EnableHotRecall {
		recallSources = append(recallSources, recallsource.NewRedisRecallSource(recallReader, "hot_recall", recallsource.HotRecall, "hot_recall"))
	}
	if cfg.EnableNewRecall {
		recallSources = append(recallSources, recallsource.NewRedisRecallSource(recallReader, "new_recall", recallsource.NewRecall, "new_recall"))
	}
	if cfg.EnableNewUGCRecall {
		recallSources = append(recallSources, recallsource.NewRedisRecallSource(recallReader, "new_ugc_recall", recallsource.NewUGC, "new_ugc_recall"))
	}
	if cfg.EnableSwingI2IRecall {
		surfaceHistory := recall.NewSurfaceHistoryStore(mq.RDB, cfg.RecallRedisNamespace)
		recallSources = append(recallSources, recallsource.NewSwingI2IRecallSource(recallReader, surfaceHistory, mq.RDB, cfg.SwingI2IRecallSeeds, cfg.SwingI2IRecallK))
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
