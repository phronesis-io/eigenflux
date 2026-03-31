package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"eigenflux_server/pipeline/consumer"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/milestone"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/telemetry"
)

func main() {
	cfg := config.Load()
	logFlush := logger.Init("pipeline/.log", "pipeline", cfg.LokiURL)
	defer logFlush()

	shutdown, err := telemetry.Init("pipeline", cfg.OtelExporterEndpoint, cfg.OtelEnabled)
	if err != nil {
		log.Fatalf("failed to init telemetry: %v", err)
	}
	defer shutdown(context.Background())

	db.Init(cfg.PgDSN)
	log.Println("PostgreSQL connected")

	mq.Init(cfg.RedisAddr, cfg.RedisPassword)
	log.Println("Redis connected")

	if err := es.InitES(cfg.EmbeddingDimensions); err != nil {
		log.Fatalf("Failed to initialize Elasticsearch: %v", err)
	}
	log.Println("Elasticsearch connected")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
		mq.RDB,
		milestoneEventIDGen,
		milestone.WithRuleCacheTTLSeconds(cfg.MilestoneRuleCacheTTL),
	)
	if err != nil {
		log.Fatalf("failed to init milestone service: %v", err)
	}

	profileConsumer := consumer.NewProfileConsumer(cfg)
	itemConsumer := consumer.NewItemConsumer(cfg)
	itemStatsConsumer := consumer.NewItemStatsConsumer(cfg, milestoneSvc)

	go profileConsumer.Start(ctx)
	go itemConsumer.Start(ctx)
	go itemStatsConsumer.Start(ctx)
	go runMilestoneRecovery(ctx, milestoneSvc)
	go runMilestoneRuleInvalidationSubscriber(ctx, milestoneSvc)

	log.Println("Pipeline started, waiting for messages...")

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down pipeline...")
	cancel()
	log.Println("Pipeline stopped")
}

func runMilestoneRecovery(ctx context.Context, svc *milestone.Service) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			recovered, err := svc.RecoverPendingNotifications(ctx, 100)
			if err != nil {
				slog.Warn("milestone recover failed", "err", err)
				continue
			}
			if recovered > 0 {
				slog.Info("milestone recover restored pending notifications", "count", recovered)
			}
		}
	}
}

func runMilestoneRuleInvalidationSubscriber(ctx context.Context, svc *milestone.Service) {
	err := milestone.SubscribeRuleInvalidation(ctx, mq.RDB, func(metricKey string) {
		if metricKey == "" {
			svc.InvalidateAllRules()
			slog.Info("milestone rule cache invalidated for all metrics")
			return
		}
		svc.InvalidateRules(metricKey)
		slog.Info("milestone rule cache invalidated", "metric", metricKey)
	})
	if err != nil && ctx.Err() == nil {
		slog.Warn("milestone rule invalidation subscriber stopped", "err", err)
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
