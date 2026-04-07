package main

import (
	"context"
	"log"
	"strings"

	"github.com/cloudwego/hertz/pkg/app/server"
	etcd "github.com/kitex-contrib/registry-etcd"

	"eigenflux_server/kitex_gen/eigenflux/auth/authservice"
	"eigenflux_server/kitex_gen/eigenflux/pm/pmservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/rpcx"
	"eigenflux_server/pkg/telemetry"
	"eigenflux_server/ws/handler"
)

func main() {
	cfg := config.Load()
	logFlush := logger.Init("WSService", cfg.EffectiveLokiURL(), cfg.LogLevel)
	defer logFlush()

	shutdown, err := telemetry.Init("WSService", cfg.OtelExporterEndpoint, cfg.MonitorEnabled)
	if err != nil {
		log.Fatalf("failed to init telemetry: %v", err)
	}
	defer shutdown(context.Background())

	// Redis (for Pub/Sub subscription).
	db.InitRedis(cfg.RedisAddr, cfg.RedisPassword)
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)

	// etcd resolver for RPC clients.
	etcdEndpoints := strings.Split(cfg.EtcdAddr, ",")
	trimmed := make([]string, 0, len(etcdEndpoints))
	for _, e := range etcdEndpoints {
		if s := strings.TrimSpace(e); s != "" {
			trimmed = append(trimmed, s)
		}
	}
	if len(trimmed) == 0 {
		trimmed = []string{"localhost:2379"}
	}

	resolver, err := etcd.NewEtcdResolver(trimmed)
	if err != nil {
		log.Fatalf("failed to create etcd resolver: %v", err)
	}

	authClient, err := authservice.NewClient("AuthService", rpcx.ClientOptions(resolver)...)
	if err != nil {
		log.Fatalf("failed to create auth client: %v", err)
	}

	pmClient, err := pmservice.NewClient("PMService", rpcx.ClientOptions(resolver)...)
	if err != nil {
		log.Fatalf("failed to create pm client: %v", err)
	}

	// Hertz HTTP server with WS route.
	wsHandler := handler.New(authClient, pmClient, db.RDB)

	listenAddr := cfg.ListenAddr(cfg.WSPort)
	h := server.Default(server.WithHostPorts(listenAddr))
	h.GET("/ws/pm", wsHandler.Serve)

	logger.Default().Info("WS service started", "addr", listenAddr)
	h.Spin()
}
