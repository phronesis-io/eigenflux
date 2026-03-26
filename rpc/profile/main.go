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

	"eigenflux_server/kitex_gen/eigenflux/profile/profileservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/logger"
)

func main() {
	cfg := config.Load()
	logger.Init("rpc/profile/.log")
	db.Init(cfg.PgDSN)

	etcdEndpoints := splitEtcdEndpoints(cfg.EtcdAddr)
	agentIDGen, err := idgen.NewManagedGenerator(context.Background(), idgen.ManagedGeneratorConfig{
		Endpoints:      etcdEndpoints,
		WorkerPrefix:   cfg.IDWorkerPrefix,
		ServiceName:    "profile-agent-id",
		InstanceID:     cfg.IDInstanceID,
		LeaseTTLSecond: cfg.IDWorkerLeaseTTL,
		EpochMS:        cfg.IDSnowflakeEpoch,
	})
	if err != nil {
		log.Fatalf("failed to init profile agent id generator: %v", err)
	}
	defer func() {
		_ = agentIDGen.Close(context.Background())
	}()
	log.Printf("profile agent id generator ready: worker_id=%d", agentIDGen.WorkerID())

	r, err := etcd.NewEtcdRegistry(etcdEndpoints)
	if err != nil {
		log.Fatalf("failed to create etcd registry: %v", err)
	}

	listenAddr := cfg.ListenAddr(cfg.ProfileRPCPort)
	addr, _ := net.ResolveTCPAddr("tcp", listenAddr)
	svr := profileservice.NewServer(
		&ProfileServiceImpl{agentIDGen: agentIDGen},
		server.WithServiceAddr(addr),
		server.WithRegistry(r),
		server.WithServerBasicInfo(&rpcinfo.EndpointBasicInfo{ServiceName: "ProfileService"}),
		server.WithMetaHandler(transmeta.ServerTTHeaderHandler),
	)

	if err := svr.Run(); err != nil {
		log.Fatalf("profile service failed: %v", err)
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
