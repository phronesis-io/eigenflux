package main

import (
	"context"
	"log"
	"net"
	"strings"

	etcd "github.com/kitex-contrib/registry-etcd"

	"eigenflux_server/kitex_gen/eigenflux/feed/feedservice"
	"eigenflux_server/kitex_gen/eigenflux/item/itemservice"
	"eigenflux_server/kitex_gen/eigenflux/sort/sortservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/rpcx"
	"eigenflux_server/pkg/telemetry"
)

var (
	sortClient sortservice.Client
	itemClient itemservice.Client
)

func main() {
	cfg := config.Load()
	logFlush := logger.Init("rpc/feed/.log", "FeedService", cfg.LokiURL)
	defer logFlush()

	shutdown, err := telemetry.Init("FeedService", cfg.OtelExporterEndpoint, cfg.OtelEnabled)
	if err != nil {
		log.Fatalf("failed to init telemetry: %v", err)
	}
	defer shutdown(context.Background())

	db.Init(cfg.PgDSN)
	db.InitRedis(cfg.RedisAddr, cfg.RedisPassword)
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)

	etcdEndpoints := splitEtcdEndpoints(cfg.EtcdAddr)

	resolver, err := etcd.NewEtcdResolver(etcdEndpoints)
	if err != nil {
		log.Fatalf("failed to create etcd resolver: %v", err)
	}

	sortClient, err = sortservice.NewClient("SortService", rpcx.ClientOptions(resolver)...)
	if err != nil {
		log.Fatalf("failed to create sort client: %v", err)
	}

	itemClient, err = itemservice.NewClient("ItemService", rpcx.ClientOptions(resolver)...)
	if err != nil {
		log.Fatalf("failed to create item client: %v", err)
	}

	registry, err := etcd.NewEtcdRegistry(etcdEndpoints)
	if err != nil {
		log.Fatalf("failed to create etcd registry: %v", err)
	}

	listenAddr := cfg.ListenAddr(cfg.FeedRPCPort)
	addr, _ := net.ResolveTCPAddr("tcp", listenAddr)
	svr := feedservice.NewServer(
		NewFeedServiceImpl(cfg),
		rpcx.ServerOptions(addr, registry, "FeedService")...,
	)

	if err := svr.Run(); err != nil {
		log.Fatalf("feed service failed: %v", err)
	}
}

func splitEtcdEndpoints(raw string) []string {
	parts := strings.Split(raw, ",")
	endpoints := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			endpoints = append(endpoints, p)
		}
	}
	if len(endpoints) == 0 {
		return []string{"localhost:2379"}
	}
	return endpoints
}
