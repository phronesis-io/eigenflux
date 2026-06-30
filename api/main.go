// @title           eigenflux_server API
// @version         1.0
// @description     EigenFlux Information Distribution Platform API
// @BasePath        /
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Enter your access token, e.g. "at_e489..."

package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	hertztracing "github.com/hertz-contrib/obs-opentelemetry/tracing"
	hertzSwagger "github.com/hertz-contrib/swagger"
	etcd "github.com/kitex-contrib/registry-etcd"
	swaggerFiles "github.com/swaggo/files"

	"eigenflux_server/api/agti"
	"eigenflux_server/api/clients"
	_ "eigenflux_server/api/docs"
	apihandler "eigenflux_server/api/handler_gen/eigenflux/api"
	"eigenflux_server/api/install"
	"eigenflux_server/api/middleware"
	router_gen "eigenflux_server/api/router_gen"
	"eigenflux_server/kitex_gen/eigenflux/auth/authservice"
	"eigenflux_server/kitex_gen/eigenflux/feed/feedservice"
	"eigenflux_server/kitex_gen/eigenflux/item/itemservice"
	"eigenflux_server/kitex_gen/eigenflux/notification/notificationservice"
	"eigenflux_server/kitex_gen/eigenflux/pm/pmservice"
	"eigenflux_server/kitex_gen/eigenflux/profile/profileservice"
	"eigenflux_server/kitex_gen/eigenflux/sort/sortservice"
	"eigenflux_server/kitex_gen/eigenflux/trade/tradeservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/publicurl"
	"eigenflux_server/pkg/rpcx"
	"eigenflux_server/pkg/skilldoc"
	"eigenflux_server/pkg/telemetry"
)

func main() {
	cfg := config.Load()
	logFlush := logger.Init("api-gateway", cfg.EffectiveLokiURL(), cfg.LogLevel)
	defer logFlush()

	shutdown, err := telemetry.Init("api-gateway", cfg.OtelExporterEndpoint, cfg.MonitorEnabled)
	if err != nil {
		log.Fatalf("failed to init telemetry: %v", err)
	}
	defer shutdown(context.Background())

	go metrics.StartMetricsServer(cfg.ApiPort + 1000)

	// Init PostgreSQL for handlers that query DB directly (e.g. feed URL enrichment).
	db.Init(cfg.PgDSN)
	log.Println("PostgreSQL connected")

	// Init Redis (for publishing stream messages)
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)
	log.Println("Redis connected")

	// Init etcd resolver
	r, err := etcd.NewEtcdResolver([]string{cfg.EtcdAddr})
	if err != nil {
		log.Fatalf("failed to create etcd resolver: %v", err)
	}

	// Init kitex clients
	profileClient, err := profileservice.NewClient("ProfileService", rpcx.ClientOptions(r)...)
	if err != nil {
		log.Fatalf("failed to create profile client: %v", err)
	}
	log.Println("Profile RPC client initialized")

	itemClient, err := itemservice.NewClient("ItemService", rpcx.ClientOptions(r)...)
	if err != nil {
		log.Fatalf("failed to create item client: %v", err)
	}
	log.Println("Item RPC client initialized")

	feedClient, err := feedservice.NewClient("FeedService", rpcx.ClientOptions(r)...)
	if err != nil {
		log.Fatalf("failed to create feed client: %v", err)
	}
	log.Println("Feed RPC client initialized")

	authClient, err := authservice.NewClient("AuthService", rpcx.ClientOptions(r)...)
	if err != nil {
		log.Fatalf("failed to create auth client: %v", err)
	}
	log.Println("Auth RPC client initialized")

	pmClient, err := pmservice.NewClient("PMService", rpcx.ClientOptions(r)...)
	if err != nil {
		log.Fatalf("failed to create pm client: %v", err)
	}
	log.Println("PM RPC client initialized")

	notificationClient, err := notificationservice.NewClient("NotificationService", rpcx.ClientOptions(r)...)
	if err != nil {
		log.Fatalf("failed to create notification client: %v", err)
	}
	log.Println("Notification RPC client initialized")

	tradeClient, err := tradeservice.NewClient("TradeService", rpcx.ClientOptions(r)...)
	if err != nil {
		log.Fatalf("failed to create trade client: %v", err)
	}
	log.Println("Trade RPC client initialized")

	sortClient, err := sortservice.NewClient("SortService", rpcx.ClientOptions(r)...)
	if err != nil {
		log.Fatalf("failed to create sort client: %v", err)
	}
	log.Println("Sort RPC client initialized")

	// Wire RPC clients for generated handlers
	clients.ProfileClient = profileClient
	clients.ItemClient = itemClient
	clients.FeedClient = feedClient
	clients.AuthClient = authClient
	clients.PMClient = pmClient
	clients.NotificationClient = notificationClient
	clients.TradeClient = tradeClient
	clients.SortClient = sortClient

	publicBaseURL := publicurl.Resolve(cfg.PublicBaseURL, cfg.ApiPort)
	skillDocs, err := skilldoc.RenderAllTemplates(skilldoc.TemplateData{
		PublicBaseURL: publicBaseURL,
		ProjectName:   cfg.ProjectName,
		ProjectTitle:  cfg.ProjectTitle,
		Description:   skilldoc.BuildDescription(cfg.ProjectName, cfg.ProjectTitle),
	})
	if err != nil {
		log.Fatalf("failed to render skill documents: %v", err)
	}
	log.Printf("Skill doc version: %s (%d reference modules)", skilldoc.Version, len(skillDocs.References))

	// Init Hertz
	listenAddr := cfg.ListenAddr(cfg.ApiPort)
	tracer, tracerCfg := hertztracing.NewServerTracer()
	h := server.Default(
		server.WithHostPorts(listenAddr),
		tracer,
	)
	h.Use(hertztracing.ServerMiddleware(tracerCfg))
	h.Use(middleware.TraceIDMiddleware())
	h.Use(metrics.HertzMiddleware())

	// Skill document endpoints. All return text/markdown with version header.
	serveSkillDoc := func(content []byte) app.HandlerFunc {
		return func(_ context.Context, c *app.RequestContext) {
			if v := c.GetHeader("X-Skill-Ver"); len(v) > 0 {
				log.Printf("Skill request from version: %s", string(v))
			}
			c.Header("X-Skill-Ver", skilldoc.Version)
			c.Data(http.StatusOK, "text/markdown; charset=utf-8", content)
		}
	}
	h.GET("/skill.md", serveSkillDoc(skillDocs.Main))
	for name, content := range skillDocs.References {
		h.GET("/references/"+name+".md", serveSkillDoc(content))
	}
	h.StaticFile("/bootstrap.md", "static/BOOTSTRAP.md")
	h.StaticFile("/install.sh", "static/install.sh")
	h.StaticFile("/install.ps1", "static/install.ps1")

	// Swagger UI
	h.GET("/swagger/*any", hertzSwagger.WrapHandler(swaggerFiles.Handler))

	// Agent-authenticated settings sync (read + agent-reported push). Registered
	// manually to reuse AuthMiddleware without an IDL/router regen.
	h.GET("/api/v1/agents/me/settings", middleware.AuthMiddleware(), apihandler.GetMySettings)
	// ClientInfoMiddleware parses X-Client-Model (and friends) into ctx so the
	// agent's reported runtime model is persisted. Generated routes get this via
	// rootMw, but this route is hand-registered, so attach it explicitly.
	h.PUT("/api/v1/agents/me/settings", middleware.ClientInfoMiddleware(), middleware.AuthMiddleware(), apihandler.PutMySettings)
	// Beat coverage: per-keyword signal/push/keep stats for the Profile page.
	h.GET("/api/v1/agents/me/beat_coverage", middleware.AuthMiddleware(), apihandler.GetBeatCoverage)
	// Messages: total/per-origin unread + mark-conversation-read.
	h.GET("/api/v1/pm/unread", middleware.AuthMiddleware(), apihandler.GetUnreadBreakdown)
	h.POST("/api/v1/pm/read", middleware.AuthMiddleware(), apihandler.MarkConvRead)

	// Broadcasts: 7-day influence leaderboard + the caller's rated broadcasts.
	h.GET("/api/v1/broadcasts/leaderboard", middleware.AuthMiddleware(), apihandler.BroadcastLeaderboard)
	h.GET("/api/v1/broadcasts/rated", middleware.AuthMiddleware(), apihandler.MyRatedItems)

	// AgentRapport quiz (public marketing activity at /agti). Registered
	// manually like the settings routes above; public by design (no auth).
	agtiBank, err := agti.LoadBank("static/agti")
	if err != nil {
		log.Fatalf("failed to load agti bank: %v", err)
	}
	// AGTI activity runs on its own domain (ICP-备案 的 www.eigenflux.net) so the
	// whole funnel — skills/answer/result/join/interpret links — stays on .net,
	// independent of the product-wide publicBaseURL (.ai). Override via AGTI_BASE_URL.
	agtiBaseURL := os.Getenv("AGTI_BASE_URL")
	if agtiBaseURL == "" {
		agtiBaseURL = "https://www.eigenflux.net"
	}
	if err := agti.InitSkills("static/templates/agti_skills.tmpl.md", "static/templates/agti_join.tmpl.md", "static/templates/agti_interpret.tmpl.md", agtiBaseURL); err != nil {
		log.Fatalf("failed to init agti skills: %v", err)
	}
	agti.Register(h, agtiBank, agtiBaseURL)
	log.Printf("AgentRapport quiz registered (%d questions, %d types)", len(agtiBank.Items), len(agtiBank.Types))

	// Install attribution (public marketing landing page at /install). Mints
	// invite tokens carrying UTM data and records install conversions. Manually
	// registered like agti above; public by design (no auth).
	install.Register(h, publicBaseURL)
	log.Print("Install attribution registered")

	// Register generated routes
	router_gen.GeneratedRegister(h)

	log.Printf("API gateway starting on %s", listenAddr)
	log.Printf("API base URL: %s", skilldoc.BuildAPIBaseURL(publicBaseURL))
	h.Spin()
}
