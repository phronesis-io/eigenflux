package main

import (
	"context"
	"eigenflux_server/rpc/sort/legacy"
	"log"
	"net"

	etcd "github.com/kitex-contrib/registry-etcd"

	"eigenflux_server/kitex_gen/eigenflux/sort/sortservice"
	"eigenflux_server/pkg/commissionindex"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/rpcx"
	"eigenflux_server/pkg/telemetry"
)

func main() {
	cfg := config.Load()
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

	legacyService := legacy.New(cfg, db.DB, mq.RDB)
	defer legacyService.Close()

	// Initialize Elasticsearch
	if err := es.InitES(cfg.EmbeddingDimensions); err != nil {
		log.Fatalf("failed to initialize ES: %v", err)
	}
	if cfg.EnableCommissionIndex {
		if err := (commissionindex.ESStore{Index: cfg.CommissionIndexName, Alias: cfg.CommissionIndexAlias, Dimensions: cfg.EmbeddingDimensions}).Ensure(context.Background()); err != nil {
			log.Fatalf("failed to bootstrap Commission index: %v", err)
		}
	}

	discoveryService, closeDiscovery, err := initDiscovery(context.Background(), cfg, legacyService.DiscoveryPolicies)
	if err != nil {
		log.Fatalf("failed to initialize discovery: %v", err)
	}
	defer closeDiscovery()
	r, err := etcd.NewEtcdRegistry([]string{cfg.EtcdAddr})
	if err != nil {
		log.Fatalf("failed to create etcd registry: %v", err)
	}

	listenAddr := cfg.ListenAddr(cfg.SortRPCPort)
	addr, _ := net.ResolveTCPAddr("tcp", listenAddr)
	svr := sortservice.NewServer(
		&SortServiceESImpl{Service: legacyService, discovery: discoveryService},
		rpcx.ServerOptions(addr, r, "SortService", metrics.KitexServerMW())...,
	)

	logger.Default().Info("sort service started", "addr", listenAddr)
	if err := svr.Run(); err != nil {
		log.Fatalf("sort service failed: %v", err)
	}
}
