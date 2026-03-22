package main

import (
	"context"
	"log"
	"net"
	"strings"
	"time"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/server"
	etcd "github.com/kitex-contrib/registry-etcd"

	"eigenflux_server/kitex_gen/eigenflux/feed/feedservice"
	"eigenflux_server/kitex_gen/eigenflux/item/itemservice"
	"eigenflux_server/kitex_gen/eigenflux/sort/sortservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/milestone"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/notification"
)

var (
	sortClient sortservice.Client
	itemClient itemservice.Client
)

func main() {
	cfg := config.Load()
	logger.Init("rpc/feed/.log")

	// Initialize database connection
	db.Init(cfg.PgDSN)
	db.InitRedis(cfg.RedisAddr, cfg.RedisPassword)
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)

	etcdEndpoints := splitEtcdEndpoints(cfg.EtcdAddr)
	milestoneEventIDGen, err := idgen.NewManagedGenerator(context.Background(), idgen.ManagedGeneratorConfig{
		Endpoints:      etcdEndpoints,
		WorkerPrefix:   cfg.IDWorkerPrefix,
		ServiceName:    "milestone-event-id",
		InstanceID:     cfg.IDInstanceID,
		LeaseTTLSecond: cfg.IDWorkerLeaseTTL,
		EpochMS:        cfg.IDSnowflakeEpoch,
	})
	if err != nil {
		log.Fatalf("failed to init milestone event id generator: %v", err)
	}
	defer func() {
		_ = milestoneEventIDGen.Close(context.Background())
	}()
	milestoneSvc, err := milestone.NewService(
		db.DB,
		db.RDB,
		milestoneEventIDGen,
		milestone.WithRuleCacheTTLSeconds(cfg.MilestoneRuleCacheTTL),
	)
	if err != nil {
		log.Fatalf("failed to init milestone service: %v", err)
	}

	// Init notification service for system notifications
	notifSvc := notification.NewService(db.DB, db.RDB)
	if err := notifSvc.RecoverActiveNotifications(context.Background()); err != nil {
		log.Printf("[Feed] Warning: failed to recover active system notifications: %v", err)
	}

	// Create etcd resolver for service discovery
	resolver, err := etcd.NewEtcdResolver(etcdEndpoints)
	if err != nil {
		log.Fatalf("failed to create etcd resolver: %v", err)
	}

	// Initialize SortService client
	sortClient, err = sortservice.NewClient(
		"SortService",
		client.WithResolver(resolver),
		client.WithRPCTimeout(3*time.Second),
	)
	if err != nil {
		log.Fatalf("failed to create sort client: %v", err)
	}

	// Initialize ItemService client
	itemClient, err = itemservice.NewClient(
		"ItemService",
		client.WithResolver(resolver),
		client.WithRPCTimeout(3*time.Second),
	)
	if err != nil {
		log.Fatalf("failed to create item client: %v", err)
	}

	// Create etcd registry for this service
	registry, err := etcd.NewEtcdRegistry(etcdEndpoints)
	if err != nil {
		log.Fatalf("failed to create etcd registry: %v", err)
	}

	listenAddr := cfg.ListenAddr(cfg.FeedRPCPort)
	addr, _ := net.ResolveTCPAddr("tcp", listenAddr)
	svr := feedservice.NewServer(
		NewFeedServiceImpl(cfg, milestoneSvc, notifSvc),
		server.WithServiceAddr(addr),
		server.WithRegistry(registry),
		server.WithServerBasicInfo(&rpcinfo.EndpointBasicInfo{ServiceName: "FeedService"}),
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
