package main

import (
	"context"
	"log"
	"net"
	"strings"

	etcd "github.com/kitex-contrib/registry-etcd"

	"eigenflux_server/kitex_gen/eigenflux/auth/authservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/email"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/rpcx"
)

// cfg is package-level for shared runtime config.
var cfg *config.Config

func main() {
	cfg = config.Load()
	logger.Init("rpc/auth/.log")
	db.Init(cfg.PgDSN)
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)

	etcdEndpoints := splitEtcdEndpoints(cfg.EtcdAddr)
	agentIDGen, err := idgen.NewManagedGenerator(context.Background(), idgen.ManagedGeneratorConfig{
		Endpoints:      etcdEndpoints,
		WorkerPrefix:   cfg.IDWorkerPrefix,
		ServiceName:    "auth-agent-id",
		InstanceID:     cfg.IDInstanceID,
		LeaseTTLSecond: cfg.IDWorkerLeaseTTL,
		EpochMS:        cfg.IDSnowflakeEpoch,
	})
	if err != nil {
		log.Fatalf("failed to init agent id generator: %v", err)
	}
	defer func() {
		_ = agentIDGen.Close(context.Background())
	}()
	log.Printf("auth agent id generator ready: worker_id=%d", agentIDGen.WorkerID())

	var emailSender email.Sender
	if cfg.EnableEmailVerification {
		if strings.TrimSpace(cfg.ResendApiKey) == "" || strings.TrimSpace(cfg.ResendFromEmail) == "" {
			log.Fatalf("invalid configuration: RESEND_API_KEY and RESEND_FROM_EMAIL are required when ENABLE_EMAIL_VERIFICATION=true")
		}
		emailSender = email.NewResendSender(cfg.ResendApiKey, cfg.ResendFromEmail)
		log.Printf("auth email sender: resend (env=%s)", strings.ToLower(strings.TrimSpace(cfg.AppEnv)))
	} else {
		log.Printf("auth email verification disabled: login will issue sessions directly")
	}

	r, err := etcd.NewEtcdRegistry(etcdEndpoints)
	if err != nil {
		log.Fatalf("failed to create etcd registry: %v", err)
	}

	listenAddr := cfg.ListenAddr(cfg.AuthRPCPort)
	addr, _ := net.ResolveTCPAddr("tcp", listenAddr)
	svr := authservice.NewServer(
		&AuthServiceImpl{
			emailSender:              emailSender,
			emailVerificationEnabled: cfg.EnableEmailVerification,
			mockUniversalOTP:         strings.TrimSpace(cfg.MockUniversalOTP),
			mockOTPEmailSuffix:       cfg.MockOTPEmailSuffixes,
			mockOTPIPWhitelist:       cfg.MockOTPIPWhitelist,
			agentIDGen:               agentIDGen,
		},
		rpcx.ServerOptions(addr, r, "AuthService")...,
	)

	if err := svr.Run(); err != nil {
		log.Fatalf("auth service failed: %v", err)
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
