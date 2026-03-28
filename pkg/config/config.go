package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"eigenflux_server/pkg/embeddingmeta"

	"github.com/joho/godotenv"
)

const (
	defaultProjectName  = "myhub"
	defaultProjectTitle = "MyHub"
)

type Config struct {
	EtcdAddr                string
	PgDSN                   string
	RedisAddr               string
	RedisPassword           string
	ProjectName             string
	ProjectTitle            string
	PublicBaseURL           string
	ESUsername              string
	ESPassword              string
	IDWorkerPrefix          string // etcd prefix for snowflake worker allocation
	IDSnowflakeEpoch        int64  // custom epoch (milliseconds)
	IDWorkerLeaseTTL        int    // etcd lease TTL for worker id
	IDInstanceID            string // optional stable instance id for worker registration
	AppEnv                  string // "dev" | "test" | "staging" | "prod"
	ApiPort                 int
	ConsoleApiPort          int
	ConsoleWebappPort       int
	ProfileRPCPort          int
	ItemRPCPort             int
	SortRPCPort             int
	FeedRPCPort             int
	AuthRPCPort             int
	PMRPCPort               int
	NotificationRPCPort     int
	LLMApiKey               string
	LLMBaseURL              string
	LLMModel                string
	EmbeddingProvider       string // "openai" or "ollama"
	EmbeddingApiKey         string
	EmbeddingBaseURL        string
	EmbeddingModel          string
	EmbeddingDimensions     int
	ResendApiKey            string
	ResendFromEmail         string
	EnableEmailVerification bool     // Whether login requires OTP email verification
	MockUniversalOTP        string   // fixed OTP for whitelist-matched requests
	ESReplicas              int      // Elasticsearch number_of_replicas
	ESShards                int      // Elasticsearch number_of_shards
	EnableSearchCache       bool     // Enable search result caching
	SearchCacheTTL          int      // Search cache TTL in seconds (default: 2)
	ProfileCacheTTL         int      // Profile cache TTL in seconds (default: 60)
	MilestoneRuleCacheTTL   int      // Milestone rule cache TTL in seconds (default: 60)
	DisableDedupInTest      bool     // Disable deduplication in dev/test environments
	QualityThreshold        float64  // Quality score threshold for filtering items (default: 0.40)
	ItemConsumerWorkers     int      // Number of concurrent workers for item consumer (default: 10)
	FeedbackConsumerWorkers int      // Number of concurrent workers for item stats consumer (default: 5)
	MockOTPEmailSuffixes    []string // Email suffixes that use mock OTP (e.g. ["@test.com"])
	MockOTPIPWhitelist      []string // IP whitelist for mock OTP
	OtelEnabled             bool     // Enable OpenTelemetry tracing
	OtelExporterEndpoint    string   // OTLP gRPC endpoint (default localhost:4317)
	LokiURL                 string   // Loki push API URL (default http://localhost:3100)
}

func Load() *Config {
	// Load .env if present (won't override existing env vars).
	loadDotEnv()

	postgresPort := getEnv("POSTGRES_PORT", "5432")
	redisPort := getEnv("REDIS_PORT", "6379")
	etcdPort := getEnv("ETCD_PORT", "2379")
	embeddingProvider := getEnv("EMBEDDING_PROVIDER", "openai")
	embeddingModel := getEnv("EMBEDDING_MODEL", "text-embedding-3-small")
	embeddingDimensions, _ := embeddingmeta.ResolveDimensions(
		embeddingProvider,
		embeddingModel,
		getEnvInt("EMBEDDING_DIMENSIONS", 0),
	)

	return &Config{
		EtcdAddr:                getEnv("ETCD_ADDR", "localhost:"+etcdPort),
		PgDSN:                   getEnv("PG_DSN", "postgres://eigenflux:eigenflux123@localhost:"+postgresPort+"/eigenflux?sslmode=disable"),
		RedisAddr:               getEnv("REDIS_ADDR", "localhost:"+redisPort),
		RedisPassword:           getEnv("REDIS_PASSWORD", ""),
		ProjectName:             getEnv("PROJECT_NAME", defaultProjectName),
		ProjectTitle:            getEnv("PROJECT_TITLE", defaultProjectTitle),
		PublicBaseURL:           getEnv("PUBLIC_BASE_URL", ""),
		ESUsername:              getEnv("ES_USERNAME", ""),
		ESPassword:              getEnv("ES_PASSWORD", ""),
		IDWorkerPrefix:          getEnv("ID_WORKER_PREFIX", "/eigenflux/idgen/workers"),
		IDSnowflakeEpoch:        getEnvInt64("ID_SNOWFLAKE_EPOCH_MS", 1704067200000), // 2024-01-01 00:00:00 UTC
		IDWorkerLeaseTTL:        getEnvInt("ID_WORKER_LEASE_TTL", 30),
		IDInstanceID:            getEnv("ID_INSTANCE_ID", ""),
		AppEnv:                  getEnv("APP_ENV", "dev"),
		ApiPort:                 getEnvInt("API_PORT", 8080),
		ConsoleApiPort:          getEnvInt("CONSOLE_API_PORT", 8090),
		ConsoleWebappPort:       getEnvInt("CONSOLE_WEBAPP_PORT", 5173),
		ProfileRPCPort:          getEnvInt("PROFILE_RPC_PORT", 8881),
		ItemRPCPort:             getEnvInt("ITEM_RPC_PORT", 8882),
		SortRPCPort:             getEnvInt("SORT_RPC_PORT", 8883),
		FeedRPCPort:             getEnvInt("FEED_RPC_PORT", 8884),
		PMRPCPort:               getEnvInt("PM_RPC_PORT", 8885),
		AuthRPCPort:             getEnvInt("AUTH_RPC_PORT", 8886),
		NotificationRPCPort:     getEnvInt("NOTIFICATION_RPC_PORT", 8887),
		LLMApiKey:               getEnv("LLM_API_KEY", ""),
		LLMBaseURL:              getEnv("LLM_BASE_URL", "https://api.openai.com/v1"),
		LLMModel:                getEnv("LLM_MODEL", "gpt-4o-mini"),
		EmbeddingProvider:       embeddingProvider,
		EmbeddingApiKey:         getEnv("EMBEDDING_API_KEY", ""),
		EmbeddingBaseURL:        getEnv("EMBEDDING_BASE_URL", ""),
		EmbeddingModel:          embeddingModel,
		EmbeddingDimensions:     embeddingDimensions,
		ResendApiKey:            getEnv("RESEND_API_KEY", ""),
		ResendFromEmail:         getEnv("RESEND_FROM_EMAIL", "noreply@example.com"),
		EnableEmailVerification: getEnvBool("ENABLE_EMAIL_VERIFICATION", false),
		MockUniversalOTP:        getEnv("MOCK_UNIVERSAL_OTP", "123456"),
		ESReplicas:              getEnvInt("ES_REPLICAS", 0),
		ESShards:                getEnvInt("ES_SHARDS", 1),
		EnableSearchCache:       getEnvBool("ENABLE_SEARCH_CACHE", true),
		SearchCacheTTL:          getEnvInt("SEARCH_CACHE_TTL", 2),
		ProfileCacheTTL:         getEnvInt("PROFILE_CACHE_TTL", 60),
		MilestoneRuleCacheTTL:   getEnvInt("MILESTONE_RULE_CACHE_TTL", 60),
		DisableDedupInTest:      getEnvBool("DISABLE_DEDUP_IN_TEST", false),
		QualityThreshold:        getEnvFloat("QUALITY_THRESHOLD", 0.0),
		ItemConsumerWorkers:     getEnvInt("ITEM_CONSUMER_WORKERS", 10),
		FeedbackConsumerWorkers: getEnvInt("FEEDBACK_CONSUMER_WORKERS", 5),
		MockOTPEmailSuffixes:    getEnvStringList("MOCK_OTP_EMAIL_SUFFIXES", nil),
		MockOTPIPWhitelist:      getEnvStringList("MOCK_OTP_IP_WHITELIST", nil),
		OtelEnabled:             getEnvBool("OTEL_ENABLED", true),
		OtelExporterEndpoint:    getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		LokiURL:                 getEnv("LOKI_URL", "http://localhost:3100"),
	}
}

func loadDotEnv() {
	if err := godotenv.Load(); err == nil {
		return
	}

	wd, err := os.Getwd()
	if err != nil {
		return
	}

	for dir := wd; ; dir = filepath.Dir(dir) {
		envPath := filepath.Join(dir, ".env")
		if _, err := os.Stat(envPath); err == nil {
			_ = godotenv.Load(envPath)
			return
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
	}
}

func IsProdEnv(appEnv string) bool {
	env := strings.ToLower(strings.TrimSpace(appEnv))
	return env == "prod" || env == "production"
}

func IsTestEnv(appEnv string) bool {
	env := strings.ToLower(strings.TrimSpace(appEnv))
	return env == "test"
}

func (c *Config) IsProd() bool {
	return IsProdEnv(c.AppEnv)
}

func (c *Config) IsTest() bool {
	return IsTestEnv(c.AppEnv)
}

func (c *Config) IsDev() bool {
	env := strings.ToLower(strings.TrimSpace(c.AppEnv))
	return env == "dev" || env == "development" || env == ""
}

// ShouldDisableDedup returns true if deduplication should be disabled
// Effective in dev or test environments when DISABLE_DEDUP_IN_TEST=true
// Always returns false in production
func (c *Config) ShouldDisableDedup() bool {
	if c == nil {
		return false
	}
	if c.IsProd() {
		return false
	}
	return (c.IsDev() || c.IsTest()) && c.DisableDedupInTest
}

func (c *Config) ListenAddr(port int) string {
	return fmt.Sprintf(":%d", port)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvInt64(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.ParseInt(v, 10, 64); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func getEnvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

// getEnvStringList parses a comma-separated env var into a string slice.
// Each element is trimmed and lowercased. Empty elements are skipped.
func getEnvStringList(key string, fallback []string) []string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parts := strings.Split(v, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			result = append(result, p)
		}
	}
	if len(result) == 0 {
		return fallback
	}
	return result
}
