package main

import (
	"context"
	"log"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"eigenflux_server/pkg/bloomfilter"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/telemetry"
	"eigenflux_server/rpc/sort/ranker"
)

var (
	cfg     *config.Config
	bf      *bloomfilter.BloomFilter
	baseCfg *ranker.RankerConfig
)

func main() {
	cfg = config.Load()
	logFlush := logger.Init("ReplayService", cfg.EffectiveLokiURL(), cfg.LogLevel)
	defer logFlush()

	shutdown, err := telemetry.Init("ReplayService", cfg.OtelExporterEndpoint, cfg.MonitorEnabled)
	if err != nil {
		log.Fatalf("failed to init telemetry: %v", err)
	}
	defer shutdown(context.Background())

	db.Init(cfg.PgDSN)

	mq.Init(cfg.RedisAddr, cfg.RedisPassword)
	bf = bloomfilter.NewBloomFilter(mq.RDB)

	baseCfg = ranker.NewRankerConfig(cfg)

	if err := es.InitES(cfg.EmbeddingDimensions); err != nil {
		log.Fatalf("failed to initialize ES: %v", err)
	}

	listenAddr := cfg.ListenAddr(cfg.ReplayPort)
	h := server.Default(server.WithHostPorts(listenAddr))

	h.POST("/api/v1/replay/sort", handleReplaySort)

	h.GET("/health", func(ctx context.Context, c *app.RequestContext) {
		c.JSON(consts.StatusOK, map[string]string{"status": "ok"})
	})

	logger.Default().Info("replay service started", "addr", listenAddr)
	h.Spin()
}
