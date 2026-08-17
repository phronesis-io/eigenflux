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
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pipeline/official"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/rpcx"
	"eigenflux_server/pkg/telemetry"

	etcd "github.com/kitex-contrib/registry-etcd"
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
	mq.SetDefaultStreamMaxLen(cfg.MqStreamMaxLen)
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

	// PM RPC client + official-account context for the official PM crons (#4/#5).
	officialResolver, err := etcd.NewEtcdResolver(splitEtcdEndpoints(cfg.EtcdAddr))
	if err != nil {
		log.Fatalf("failed to create etcd resolver: %v", err)
	}
	officialPMClient, err := pmservice.NewClient("PMService", rpcx.ClientOptions(officialResolver)...)
	if err != nil {
		log.Fatalf("failed to create pm client: %v", err)
	}
	officialCtxShared := official.NewSender(cfg, officialPMClient, llmClient, prompts)

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start cron jobs
	go StartAgentCountUpdater(ctx, cfg, mq.RDB)
	go StartAgentCardUpdater(ctx, cfg, mq.RDB)
	go StartStatsCalibrator(ctx, cfg, mq.RDB)
	go StartEmbeddingBackfill(ctx, cfg, mq.RDB)
	go StartSuggestionBackfill(ctx, cfg, mq.RDB, llmClient)
	go StartActivityCleanup(ctx, mq.RDB)
	go StartConsoleV2Cleanup(ctx, mq.RDB)
	profileCleanupDone := make(chan struct{})
	go func() {
		defer close(profileCleanupDone)
		StartProfileChangeCleanup(ctx, mq.RDB)
	}()
	go StartHighlightTranslate(ctx, cfg, mq.RDB, llmClient)
	go StartReplayCleanup(ctx, cfg, mq.RDB)
	if cfg.EnableOfficialTrending {
		go StartOfficialTrending(ctx, cfg, mq.RDB, officialCtxShared)
	}
	if cfg.EnableOfficialFeedRescue {
		go StartOfficialFeedRescue(ctx, cfg, mq.RDB, officialCtxShared)
	}

	log.Println("Cron service started")

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down cron service...")
	cancel()
	select {
	case <-profileCleanupDone:
	case <-time.After(30 * time.Second):
		log.Println("profile change cleanup did not stop within 30s; continuing shutdown")
	}

	log.Println("Cron service stopped")
}
