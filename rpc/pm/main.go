package main

import (
	"context"
	"errors"
	"log"
	"net"
	"os"
	"strings"

	etcd "github.com/kitex-contrib/registry-etcd"

	"eigenflux_server/kitex_gen/eigenflux/pm/pmservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/rpcx"
	"eigenflux_server/pkg/telemetry"
	"eigenflux_server/rpc/pm/icebreak"
	"eigenflux_server/rpc/pm/ratelimit"
	"eigenflux_server/rpc/pm/validator"
)

func main() {
	cfg := config.Load()
	logFlush := logger.Init("PMService", cfg.EffectiveLokiURL(), cfg.LogLevel)
	defer logFlush()

	shutdown, err := telemetry.Init("PMService", cfg.OtelExporterEndpoint, cfg.MonitorEnabled)
	if err != nil {
		log.Fatalf("failed to init telemetry: %v", err)
	}
	defer shutdown(context.Background())

	go metrics.StartMetricsServer(cfg.PMRPCPort + 1000)

	// Initialize database connection
	db.Init(cfg.PgDSN)
	db.InitRedis(cfg.RedisAddr, cfg.RedisPassword)
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)
	mq.SetDefaultStreamMaxLen(cfg.MqStreamMaxLen)

	etcdEndpoints := splitEtcdEndpoints(cfg.EtcdAddr)

	// Create conversation ID generator
	convIDGen, err := idgen.NewManagedGenerator(context.Background(), idgen.ManagedGeneratorConfig{
		Endpoints:      etcdEndpoints,
		WorkerPrefix:   cfg.IDWorkerPrefix,
		ServiceName:    "pm-conv-id",
		InstanceID:     cfg.IDInstanceID,
		LeaseTTLSecond: cfg.IDWorkerLeaseTTL,
		EpochMS:        cfg.IDSnowflakeEpoch,
	})
	if err != nil {
		log.Fatalf("failed to init conversation id generator: %v", err)
	}
	defer func() {
		_ = convIDGen.Close(context.Background())
	}()

	// Create message ID generator
	msgIDGen, err := idgen.NewManagedGenerator(context.Background(), idgen.ManagedGeneratorConfig{
		Endpoints:      etcdEndpoints,
		WorkerPrefix:   cfg.IDWorkerPrefix,
		ServiceName:    "pm-msg-id",
		InstanceID:     cfg.IDInstanceID,
		LeaseTTLSecond: cfg.IDWorkerLeaseTTL,
		EpochMS:        cfg.IDSnowflakeEpoch,
	})
	if err != nil {
		log.Fatalf("failed to init message id generator: %v", err)
	}
	defer func() {
		_ = msgIDGen.Close(context.Background())
	}()

	// Create friend request ID generator
	reqIDGen, err := idgen.NewManagedGenerator(context.Background(), idgen.ManagedGeneratorConfig{
		Endpoints:      etcdEndpoints,
		WorkerPrefix:   cfg.IDWorkerPrefix,
		ServiceName:    "pm-req-id",
		InstanceID:     cfg.IDInstanceID,
		LeaseTTLSecond: cfg.IDWorkerLeaseTTL,
		EpochMS:        cfg.IDSnowflakeEpoch,
	})
	if err != nil {
		log.Fatalf("failed to init friend request id generator: %v", err)
	}
	defer func() {
		_ = reqIDGen.Close(context.Background())
	}()

	// Create ice breaker and validator
	iceBreaker := icebreak.NewIceBreaker(db.RDB)
	pmValidator := validator.NewValidator(db.DB, db.RDB)
	friendRequestLimitsPath := ratelimit.ResolveConfigPath(cfg.IsProd())
	friendRequestLimits, err := ratelimit.LoadFile(friendRequestLimitsPath)
	if errors.Is(err, os.ErrNotExist) {
		if cfg.IsProd() {
			log.Fatalf("friend request rate-limit config not found in production: %s", friendRequestLimitsPath)
		}
		friendRequestLimits = ratelimit.DefaultConfig()
		logger.Default().Warn("friend request rate-limit config not found; using defaults", "path", friendRequestLimitsPath)
	} else if err != nil {
		log.Fatalf("failed to load friend request rate-limit config: %v", err)
	} else {
		logger.Default().Info(
			"friend request rate-limit config loaded",
			"path", friendRequestLimitsPath,
			"defaultHourlyLimit", friendRequestLimits.DefaultHourlyLimit,
			"overrideCount", len(friendRequestLimits.Overrides),
		)
	}

	// Create etcd registry for this service
	registry, err := etcd.NewEtcdRegistry(etcdEndpoints)
	if err != nil {
		log.Fatalf("failed to create etcd registry: %v", err)
	}

	listenAddr := cfg.ListenAddr(cfg.PMRPCPort)
	addr, _ := net.ResolveTCPAddr("tcp", listenAddr)
	svr := pmservice.NewServer(
		&PMServiceImpl{
			convIDGen:           convIDGen,
			msgIDGen:            msgIDGen,
			reqIDGen:            reqIDGen,
			iceBreaker:          iceBreaker,
			validator:           pmValidator,
			friendRequestLimits: friendRequestLimits,
		},
		rpcx.ServerOptions(addr, registry, "PMService", metrics.KitexServerMW())...,
	)

	logger.Default().Info("PM service started", "addr", listenAddr)
	if err := svr.Run(); err != nil {
		log.Fatalf("pm service failed: %v", err)
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
