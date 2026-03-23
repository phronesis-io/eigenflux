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
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/kitex/client"
	hertzSwagger "github.com/hertz-contrib/swagger"
	etcd "github.com/kitex-contrib/registry-etcd"
	swaggerFiles "github.com/swaggo/files"

	"eigenflux_server/api/clients"
	_ "eigenflux_server/api/docs"
	router_gen "eigenflux_server/api/router_gen"
	"eigenflux_server/kitex_gen/eigenflux/auth/authservice"
	"eigenflux_server/kitex_gen/eigenflux/feed/feedservice"
	"eigenflux_server/kitex_gen/eigenflux/item/itemservice"
	"eigenflux_server/kitex_gen/eigenflux/pm/pmservice"
	"eigenflux_server/kitex_gen/eigenflux/profile/profileservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/publicurl"
	"eigenflux_server/pkg/skilldoc"
)

func main() {
	cfg := config.Load()
	logger.Init("api/.log")

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
	profileClient, err := profileservice.NewClient("ProfileService",
		client.WithResolver(r),
		client.WithRPCTimeout(3*time.Second),
	)
	if err != nil {
		log.Fatalf("failed to create profile client: %v", err)
	}
	log.Println("Profile RPC client initialized")

	itemClient, err := itemservice.NewClient("ItemService",
		client.WithResolver(r),
		client.WithRPCTimeout(3*time.Second),
	)
	if err != nil {
		log.Fatalf("failed to create item client: %v", err)
	}
	log.Println("Item RPC client initialized")

	feedClient, err := feedservice.NewClient("FeedService",
		client.WithResolver(r),
		client.WithRPCTimeout(3*time.Second),
	)
	if err != nil {
		log.Fatalf("failed to create feed client: %v", err)
	}
	log.Println("Feed RPC client initialized")

	authClient, err := authservice.NewClient("AuthService",
		client.WithResolver(r),
		client.WithRPCTimeout(5*time.Second),
	)
	if err != nil {
		log.Fatalf("failed to create auth client: %v", err)
	}
	log.Println("Auth RPC client initialized")

	pmClient, err := pmservice.NewClient("PMService",
		client.WithResolver(r),
		client.WithRPCTimeout(3*time.Second),
	)
	if err != nil {
		log.Fatalf("failed to create pm client: %v", err)
	}
	log.Println("PM RPC client initialized")

	// Wire RPC clients for generated handlers
	clients.ProfileClient = profileClient
	clients.ItemClient = itemClient
	clients.FeedClient = feedClient
	clients.AuthClient = authClient
	clients.PMClient = pmClient

	publicBaseURL := publicurl.Resolve(cfg.PublicBaseURL, cfg.ApiPort)
	renderedSkill, err := skilldoc.RenderDefaultTemplate(skilldoc.TemplateData{
		PublicBaseURL: publicBaseURL,
		ProjectName:   cfg.ProjectName,
		ProjectTitle:  cfg.ProjectTitle,
		Description:   skilldoc.BuildDescription(cfg.ProjectName, cfg.ProjectTitle),
	})
	if err != nil {
		log.Fatalf("failed to render skill.md: %v", err)
	}

	// Init Hertz
	listenAddr := cfg.ListenAddr(cfg.ApiPort)
	h := server.Default(server.WithHostPorts(listenAddr))

	// Rendered skill document. PROJECT_NAME controls the local namespace in the
	// instructions, PROJECT_TITLE controls human-visible title copy, and
	// PUBLIC_BASE_URL controls the public root URL shown to agents.
	h.GET("/skill.md", func(_ context.Context, c *app.RequestContext) {
		c.Data(http.StatusOK, "text/markdown; charset=utf-8", renderedSkill)
	})
	h.StaticFile("/bootstrap.md", "static/BOOTSTRAP.md")

	// Swagger UI
	h.GET("/swagger/*any", hertzSwagger.WrapHandler(swaggerFiles.Handler))

	// Register generated routes
	router_gen.GeneratedRegister(h)

	log.Printf("API gateway starting on %s", listenAddr)
	log.Printf("API base URL: %s", skilldoc.BuildAPIBaseURL(publicBaseURL))
	log.Printf("Share this with your friends: 'Read %s and help me join %s'", skilldoc.BuildSkillURL(publicBaseURL), cfg.ProjectName)
	h.Spin()
}
