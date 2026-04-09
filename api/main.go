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

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	hertztracing "github.com/hertz-contrib/obs-opentelemetry/tracing"
	hertzSwagger "github.com/hertz-contrib/swagger"
	etcd "github.com/kitex-contrib/registry-etcd"
	swaggerFiles "github.com/swaggo/files"

	"eigenflux_server/api/clients"
	_ "eigenflux_server/api/docs"
	"eigenflux_server/api/middleware"
	router_gen "eigenflux_server/api/router_gen"
	"eigenflux_server/kitex_gen/eigenflux/auth/authservice"
	"eigenflux_server/kitex_gen/eigenflux/feed/feedservice"
	"eigenflux_server/kitex_gen/eigenflux/item/itemservice"
	"eigenflux_server/kitex_gen/eigenflux/notification/notificationservice"
	"eigenflux_server/kitex_gen/eigenflux/pm/pmservice"
	"eigenflux_server/kitex_gen/eigenflux/profile/profileservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
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

	// Wire RPC clients for generated handlers
	clients.ProfileClient = profileClient
	clients.ItemClient = itemClient
	clients.FeedClient = feedClient
	clients.AuthClient = authClient
	clients.PMClient = pmClient
	clients.NotificationClient = notificationClient

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

	// Register generated routes
	router_gen.GeneratedRegister(h)

	// Logout endpoint (direct DB+Redis, no IDL)
	h.POST("/api/v1/auth/logout", append([]app.HandlerFunc{middleware.AuthMiddleware()}, logoutHandler)...)

	log.Printf("API gateway starting on %s", listenAddr)
	log.Printf("API base URL: %s", skilldoc.BuildAPIBaseURL(publicBaseURL))
	log.Printf("Share this with your friends: 'Read %s and help me join %s'", skilldoc.BuildSkillURL(publicBaseURL), cfg.ProjectName)
	h.Spin()
}
