package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/telemetry"
)

func splitEtcdEndpoints(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{"localhost:2379"}
	}
	return out
}

func mustCronIDGen(endpoints []string, cfg *config.Config, name string) *idgen.ManagedGenerator {
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
	return gen
}

func main() {
	cfg := config.Load()
	logFlush := logger.Init("pipeline-cron", cfg.EffectiveLokiURL(), cfg.LogLevel)
	defer logFlush()

	shutdown, err := telemetry.Init("pipeline-cron", cfg.OtelExporterEndpoint, cfg.MonitorEnabled)
	if err != nil {
		log.Fatalf("failed to init telemetry: %v", err)
	}
	defer shutdown(context.Background())

	// Init PostgreSQL
	db.Init(cfg.PgDSN)
	log.Println("PostgreSQL connected")

	// Init Redis
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)
	log.Println("Redis connected")

	// Init Elasticsearch
	if err := es.InitES(cfg.EmbeddingDimensions); err != nil {
		log.Fatalf("Failed to initialize Elasticsearch: %v", err)
	}
	log.Println("Elasticsearch connected")

	// Init LLM client for suggestion backfill
	prompts, err := llm.LoadDefaultPrompts()
	if err != nil {
		log.Fatalf("failed to load prompt templates: %v", err)
	}
	if err := llm.ValidateAllPrompts(prompts); err != nil {
		log.Fatalf("prompt validation failed: %v", err)
	}
	llmClient := llm.NewClient(cfg, prompts)

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	etcdEndpoints := splitEtcdEndpoints(cfg.EtcdAddr)
	expiryEventIDGen := mustCronIDGen(etcdEndpoints, cfg, "trade-expiry-event-id")
	defer func() { _ = expiryEventIDGen.Close(context.Background()) }()
	expiryOutboxIDGen := mustCronIDGen(etcdEndpoints, cfg, "trade-expiry-outbox-id")
	defer func() { _ = expiryOutboxIDGen.Close(context.Background()) }()
	expiryScanner := NewExpiryScanner(expiryEventIDGen, expiryOutboxIDGen)

	// Start cron jobs
	go StartAgentCountUpdater(ctx, cfg, mq.RDB)
	go StartStatsCalibrator(ctx, cfg, mq.RDB)
	go StartEmbeddingBackfill(ctx, cfg, mq.RDB)
	go StartSuggestionBackfill(ctx, cfg, mq.RDB, llmClient)
	go StartActivityCleanup(ctx, mq.RDB)
	go StartHighlightTranslate(ctx, cfg, mq.RDB, llmClient)
	go StartTradeExpiryScanner(ctx, cfg, mq.RDB, expiryScanner)
	go StartOutboxDispatcher(ctx, cfg)
	go StartOutboxCleanup(ctx, cfg)

	log.Println("Cron service started")

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down cron service...")
	cancel()

	log.Println("Cron service stopped")
}
