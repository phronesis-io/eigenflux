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
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/rpcx"
	"eigenflux_server/pkg/telemetry"
	"eigenflux_server/rpc/sort/ranker"
)

var bf *bloomfilter.BloomFilter
var cfg *config.Config
var searchCache *cache.SearchCache
var profileCache *cache.ProfileCache
var rankerInstance *ranker.Ranker
var rankerCfg *ranker.RankerConfig
var embeddingCache *cache.EmbeddingCache

func main() {
	cfg = config.Load()
	logFlush := logger.Init("SortService", cfg.EffectiveLokiURL(), cfg.LogLevel)
	defer logFlush()

	shutdown, err := telemetry.Init("SortService", cfg.OtelExporterEndpoint, cfg.MonitorEnabled)
	if err != nil {
		log.Fatalf("failed to init telemetry: %v", err)
	}
	defer shutdown(context.Background())

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

	// Initialize embedding cache
	embeddingCache = cache.NewEmbeddingCache(mq.RDB, 24*time.Hour)

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
		rpcx.ServerOptions(addr, r, "SortService")...,
	)

	logger.Default().Info("sort service started", "addr", listenAddr)
	if err := svr.Run(); err != nil {
		log.Fatalf("sort service failed: %v", err)
	}
}
