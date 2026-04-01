package main

import (
	"context"
	"log"
	"net"
	"strings"

	etcd "github.com/kitex-contrib/registry-etcd"

	"eigenflux_server/kitex_gen/eigenflux/item/itemservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/rpcx"
	"eigenflux_server/pkg/telemetry"
)

func main() {
	cfg := config.Load()
	logFlush := logger.Init("rpc/item/.log", "ItemService", cfg.EffectiveLokiURL())
	defer logFlush()

	shutdown, err := telemetry.Init("ItemService", cfg.OtelExporterEndpoint, cfg.MonitorEnabled)
	if err != nil {
		log.Fatalf("failed to init telemetry: %v", err)
	}
	defer shutdown(context.Background())

	db.Init(cfg.PgDSN)

	etcdEndpoints := splitEtcdEndpoints(cfg.EtcdAddr)
	itemIDGen, err := idgen.NewManagedGenerator(context.Background(), idgen.ManagedGeneratorConfig{
		Endpoints:      etcdEndpoints,
		WorkerPrefix:   cfg.IDWorkerPrefix,
		ServiceName:    "item-item-id",
		InstanceID:     cfg.IDInstanceID,
		LeaseTTLSecond: cfg.IDWorkerLeaseTTL,
		EpochMS:        cfg.IDSnowflakeEpoch,
	})
	if err != nil {
		log.Fatalf("failed to init item id generator: %v", err)
	}
	defer func() {
		_ = itemIDGen.Close(context.Background())
	}()
	log.Printf("item id generator ready: worker_id=%d", itemIDGen.WorkerID())

	r, err := etcd.NewEtcdRegistry(etcdEndpoints)
	if err != nil {
		log.Fatalf("failed to create etcd registry: %v", err)
	}

	listenAddr := cfg.ListenAddr(cfg.ItemRPCPort)
	addr, _ := net.ResolveTCPAddr("tcp", listenAddr)
	svr := itemservice.NewServer(
		&ItemServiceImpl{itemIDGen: itemIDGen},
		rpcx.ServerOptions(addr, r, "ItemService")...,
	)

	if err := svr.Run(); err != nil {
		log.Fatalf("item service failed: %v", err)
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
