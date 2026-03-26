package main

import (
	"context"
	"log"
	"net"
	"strings"

	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/transmeta"
	"github.com/cloudwego/kitex/server"
	etcd "github.com/kitex-contrib/registry-etcd"

	"eigenflux_server/kitex_gen/eigenflux/notification/notificationservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
)

func main() {
	cfg := config.Load()
	logger.Init("rpc/notification/.log")

	db.Init(cfg.PgDSN)
	db.InitRedis(cfg.RedisAddr, cfg.RedisPassword)

	impl := NewNotificationServiceImpl(db.DB, db.RDB)

	// Recover active system notifications from DB to Redis
	if err := impl.RecoverActiveNotifications(context.Background()); err != nil {
		log.Printf("[Notification] Warning: failed to recover active system notifications: %v", err)
	}

	etcdEndpoints := splitEtcdEndpoints(cfg.EtcdAddr)
	registry, err := etcd.NewEtcdRegistry(etcdEndpoints)
	if err != nil {
		log.Fatalf("failed to create etcd registry: %v", err)
	}

	listenAddr := cfg.ListenAddr(cfg.NotificationRPCPort)
	addr, _ := net.ResolveTCPAddr("tcp", listenAddr)
	svr := notificationservice.NewServer(
		impl,
		server.WithServiceAddr(addr),
		server.WithRegistry(registry),
		server.WithServerBasicInfo(&rpcinfo.EndpointBasicInfo{ServiceName: "NotificationService"}),
		server.WithMetaHandler(transmeta.ServerTTHeaderHandler),
	)

	log.Printf("[Notification] Service starting on %s", listenAddr)
	if err := svr.Run(); err != nil {
		log.Fatalf("notification service failed: %v", err)
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
