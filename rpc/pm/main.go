package main

import (
	"context"
	"log"
	"net"
	"strings"

	"github.com/cloudwego/kitex/server"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	etcd "github.com/kitex-contrib/registry-etcd"

	"eigenflux_server/kitex_gen/eigenflux/pm/pmservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/rpc/pm/icebreak"
	"eigenflux_server/rpc/pm/validator"
)

func main() {
	cfg := config.Load()
	logger.Init("rpc/pm/.log")

	// Initialize database connection
	db.Init(cfg.PgDSN)
	db.InitRedis(cfg.RedisAddr, cfg.RedisPassword)
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)

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

	// Create ice breaker and validator
	iceBreaker := icebreak.NewIceBreaker(db.RDB)
	pmValidator := validator.NewValidator(db.DB, db.RDB)

	// Create etcd registry for this service
	registry, err := etcd.NewEtcdRegistry(etcdEndpoints)
	if err != nil {
		log.Fatalf("failed to create etcd registry: %v", err)
	}

	listenAddr := cfg.ListenAddr(cfg.PMRPCPort)
	addr, _ := net.ResolveTCPAddr("tcp", listenAddr)
	svr := pmservice.NewServer(
		&PMServiceImpl{
			convIDGen:  convIDGen,
			msgIDGen:   msgIDGen,
			iceBreaker: iceBreaker,
			validator:  pmValidator,
		},
		server.WithServiceAddr(addr),
		server.WithRegistry(registry),
		server.WithServerBasicInfo(&rpcinfo.EndpointBasicInfo{ServiceName: "PMService"}),
	)

	log.Printf("PM service starting on %s", listenAddr)
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
