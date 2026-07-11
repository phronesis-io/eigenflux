package main

import (
	"context"
	"log"
	"net"
	"strings"
	"time"

	etcd "github.com/kitex-contrib/registry-etcd"

	"eigenflux_server/kitex_gen/eigenflux/trade/tradeservice"
	"eigenflux_server/pkg/chief"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/rpcx"
	"eigenflux_server/pkg/telemetry"
)

func main() {
	cfg := config.Load()
	logFlush := logger.Init("TradeService", cfg.EffectiveLokiURL(), cfg.LogLevel)
	defer logFlush()

	shutdown, err := telemetry.Init("TradeService", cfg.OtelExporterEndpoint, cfg.MonitorEnabled)
	if err != nil {
		log.Fatalf("failed to init telemetry: %v", err)
	}
	defer shutdown(context.Background())

	go metrics.StartMetricsServer(cfg.TradeRPCPort + 1000)

	db.Init(cfg.PgDSN)
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)
	mq.SetDefaultStreamMaxLen(cfg.MqStreamMaxLen)

	etcdEndpoints := splitEtcdEndpoints(cfg.EtcdAddr)

	serviceIDGen := mustIDGen(etcdEndpoints, cfg, "trade-service-id")
	defer func() { _ = serviceIDGen.Close(context.Background()) }()

	orderIDGen := mustIDGen(etcdEndpoints, cfg, "trade-order-id")
	defer func() { _ = orderIDGen.Close(context.Background()) }()

	eventIDGen := mustIDGen(etcdEndpoints, cfg, "trade-event-id")
	defer func() { _ = eventIDGen.Close(context.Background()) }()

	receiptIDGen := mustIDGen(etcdEndpoints, cfg, "trade-receipt-id")
	defer func() { _ = receiptIDGen.Close(context.Background()) }()

	outboxIDGen := mustIDGen(etcdEndpoints, cfg, "trade-outbox-id")
	defer func() { _ = outboxIDGen.Close(context.Background()) }()

	chiefClient := chief.NewClient(cfg.ChiefLedgerURL, time.Duration(cfg.ChiefHTTPTimeoutSec)*time.Second)

	r, err := etcd.NewEtcdRegistry(etcdEndpoints)
	if err != nil {
		log.Fatalf("failed to create etcd registry: %v", err)
	}

	listenAddr := cfg.ListenAddr(cfg.TradeRPCPort)
	addr, _ := net.ResolveTCPAddr("tcp", listenAddr)
	svr := tradeservice.NewServer(
		&TradeServiceImpl{
			serviceIDGen:  serviceIDGen,
			orderIDGen:    orderIDGen,
			eventIDGen:    eventIDGen,
			receiptIDGen:  receiptIDGen,
			outboxIDGen:   outboxIDGen,
			chiefClient:   chiefClient,
			chiefLookback: cfg.ChiefVerifyLookbackLimit,
			maxActive:     cfg.TradeMaxActiveOrders,
		},
		rpcx.ServerOptions(addr, r, "TradeService", metrics.KitexServerMW())...,
	)

	log.Printf("Trade RPC starting on %s", listenAddr)
	if err := svr.Run(); err != nil {
		log.Fatalf("trade service failed: %v", err)
	}
}

func mustIDGen(endpoints []string, cfg *config.Config, name string) *idgen.ManagedGenerator {
	gen, err := idgen.NewManagedGenerator(context.Background(), idgen.ManagedGeneratorConfig{
		Endpoints:      endpoints,
		WorkerPrefix:   cfg.IDWorkerPrefix,
		ServiceName:    name,
		InstanceID:     cfg.IDInstanceID,
		LeaseTTLSecond: cfg.IDWorkerLeaseTTL,
		EpochMS:        cfg.IDSnowflakeEpoch,
	})
	if err != nil {
		log.Fatalf("failed to init %s generator: %v", name, err)
	}
	log.Printf("%s generator ready: worker_id=%d", name, gen.WorkerID())
	return gen
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
