package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/pm/pmservice"
	"eigenflux_server/pipeline/consumer"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pipeline/official"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/milestone"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/rpcx"
	"eigenflux_server/pkg/telemetry"

	etcd "github.com/kitex-contrib/registry-etcd"
)

func main() {
	cfg := config.Load()
	logFlush := logger.Init("pipeline", cfg.EffectiveLokiURL(), cfg.LogLevel)
	defer logFlush()

	shutdown, err := telemetry.Init("pipeline", cfg.OtelExporterEndpoint, cfg.MonitorEnabled)
	if err != nil {
		log.Fatalf("failed to init telemetry: %v", err)
	}
	defer shutdown(context.Background())

	go metrics.StartMetricsServer(9070)

	db.Init(cfg.PgDSN)
	log.Println("PostgreSQL connected")

	mq.Init(cfg.RedisAddr, cfg.RedisPassword)
	log.Println("Redis connected")

	if err := es.InitES(cfg.EmbeddingDimensions); err != nil {
		log.Fatalf("Failed to initialize Elasticsearch: %v", err)
	}
	log.Println("Elasticsearch connected")

	// PM RPC client: the official welcome consumer sends private messages as the
	// official account by reusing PMService.SendPM (conversation creation, friend
	// fast-path, push) rather than reimplementing it against the DAL.
	resolver, err := etcd.NewEtcdResolver([]string{cfg.EtcdAddr})
	if err != nil {
		log.Fatalf("failed to create etcd resolver: %v", err)
	}
	pmClient, err := pmservice.NewClient("PMService", rpcx.ClientOptions(resolver)...)
	if err != nil {
		log.Fatalf("failed to create pm client: %v", err)
	}
	log.Println("PM RPC client initialized")

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

	prompts, err := llm.LoadDefaultPrompts()
	if err != nil {
		log.Fatalf("failed to load prompt templates: %v", err)
	}
	if err := llm.ValidateAllPrompts(prompts); err != nil {
		log.Fatalf("prompt validation failed: %v", err)
	}
	log.Printf("Loaded and validated %d prompt templates: %v", len(prompts.Names()), prompts.Names())

	// Shared official-account sender for the consumers that act as the official
	// account (welcome, first-broadcast reply, inbox chat).
	officialSender := official.NewSender(cfg, pmClient, llm.NewClient(cfg, prompts), prompts)

	profileConsumer := consumer.NewProfileConsumer(cfg, prompts)
	itemConsumer := consumer.NewItemConsumer(cfg, prompts)
	itemStatsConsumer := consumer.NewItemStatsConsumer(cfg, milestoneSvc)
	serviceConsumer := consumer.NewServiceConsumer(cfg, prompts)
	orderEventConsumer := consumer.NewOrderEventConsumer()

	var replayConsumer *consumer.ReplayConsumer
	if cfg.EnableReplayLog {
		replayIDGen, err := idgen.NewManagedGenerator(context.Background(), idgen.ManagedGeneratorConfig{
			Endpoints:      etcdEndpoints,
			WorkerPrefix:   cfg.IDWorkerPrefix,
			ServiceName:    "replay-log-id",
			InstanceID:     cfg.IDInstanceID,
			LeaseTTLSecond: cfg.IDWorkerLeaseTTL,
			EpochMS:        cfg.IDSnowflakeEpoch,
		})
		if err != nil {
			log.Fatalf("failed to init replay log id generator: %v", err)
		}
		defer func() {
			_ = replayIDGen.Close(context.Background())
		}()
		replayConsumer = consumer.NewReplayConsumer(replayIDGen)
	}

	activityIDGen, err := idgen.NewManagedGenerator(context.Background(), idgen.ManagedGeneratorConfig{
		Endpoints:      etcdEndpoints,
		WorkerPrefix:   cfg.IDWorkerPrefix,
		ServiceName:    "activity-log-id",
		InstanceID:     cfg.IDInstanceID,
		LeaseTTLSecond: cfg.IDWorkerLeaseTTL,
		EpochMS:        cfg.IDSnowflakeEpoch,
	})
	if err != nil {
		log.Fatalf("failed to init activity log id generator: %v", err)
	}
	defer func() {
		_ = activityIDGen.Close(context.Background())
	}()
	activityConsumer := consumer.NewActivityConsumer(activityIDGen)

	var officialWelcomeConsumer *consumer.OfficialWelcomeConsumer
	if cfg.EnableOfficialWelcome {
		officialWelcomeConsumer = consumer.NewOfficialWelcomeConsumer(cfg, officialSender)
	}

	go profileConsumer.Start(ctx)
	go itemConsumer.Start(ctx)
	go itemStatsConsumer.Start(ctx)
	go serviceConsumer.Start(ctx)
	go orderEventConsumer.Start(ctx)
	go runMilestoneRecovery(ctx, milestoneSvc)
	go runMilestoneRuleInvalidationSubscriber(ctx, milestoneSvc)
	if replayConsumer != nil {
		go replayConsumer.Start(ctx)
	}
	go activityConsumer.Start(ctx)
	if officialWelcomeConsumer != nil {
		go officialWelcomeConsumer.Start(ctx)
	}

	lagGroups := []metrics.StreamGroup{
		{Stream: "stream:profile:update", Group: "cg:profile:update"},
		{Stream: "stream:item:publish", Group: "cg:item:publish"},
		{Stream: "stream:item:stats", Group: "cg:item:stats"},
		{Stream: "stream:replay:log", Group: "cg:replay:log"},
		{Stream: "stream:agent:activity", Group: "cg:agent:activity"},
		{Stream: "stream:trade:service", Group: "cg:trade:service"},
		{Stream: "stream:trade:order-event", Group: "cg:trade:order-event"},
	}
	if cfg.EnableOfficialWelcome {
		lagGroups = append(lagGroups, metrics.StreamGroup{Stream: "stream:profile:update", Group: "cg:official:welcome"})
	}
	go metrics.StartLagPoller(ctx, mq.RDB, lagGroups, 10*time.Second)

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
				logger.Default().Warn("milestone recover failed", "err", err)
				continue
			}
			if recovered > 0 {
				logger.Default().Info("milestone recover restored pending notifications", "count", recovered)
			}
		}
	}
}

func runMilestoneRuleInvalidationSubscriber(ctx context.Context, svc *milestone.Service) {
	err := milestone.SubscribeRuleInvalidation(ctx, mq.RDB, func(metricKey string) {
		if metricKey == "" {
			svc.InvalidateAllRules()
			logger.Default().Info("milestone rule cache invalidated for all metrics")
			return
		}
		svc.InvalidateRules(metricKey)
		logger.Default().Info("milestone rule cache invalidated", "metric", metricKey)
	})
	if err != nil && ctx.Err() == nil {
		logger.Default().Warn("milestone rule invalidation subscriber stopped", "err", err)
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
