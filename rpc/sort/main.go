package main

import (
	"log"
	"net"
	"time"

	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/transmeta"
	"github.com/cloudwego/kitex/server"
	etcd "github.com/kitex-contrib/registry-etcd"

	"eigenflux_server/kitex_gen/eigenflux/sort/sortservice"
	"eigenflux_server/pkg/bloomfilter"
	"eigenflux_server/pkg/cache"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
)

var bf *bloomfilter.BloomFilter
var cfg *config.Config
var searchCache *cache.SearchCache
var profileCache *cache.ProfileCache

func main() {
	cfg = config.Load()
	logger.Init("rpc/sort/.log")

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
		log.Printf("Cache enabled: search_ttl=%ds, profile_ttl=%ds", cfg.SearchCacheTTL, cfg.ProfileCacheTTL)
	}

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
		new(SortServiceESImpl), // Use ES implementation
		server.WithServiceAddr(addr),
		server.WithRegistry(r),
		server.WithServerBasicInfo(&rpcinfo.EndpointBasicInfo{ServiceName: "SortService"}),
		server.WithMetaHandler(transmeta.ServerTTHeaderHandler),
	)

	log.Printf("Sort service (ES) started on %s", listenAddr)
	if err := svr.Run(); err != nil {
		log.Fatalf("sort service failed: %v", err)
	}
}
