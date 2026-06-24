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
	EtcdAddr                    string
	PgDSN                       string
	RedisAddr                   string
	RedisPassword               string
	ProjectName                 string
	ProjectTitle                string
	PublicBaseURL               string
	ESUsername                  string
	ESPassword                  string
	IDWorkerPrefix              string // etcd prefix for snowflake worker allocation
	IDSnowflakeEpoch            int64  // custom epoch (milliseconds)
	IDWorkerLeaseTTL            int    // etcd lease TTL for worker id
	IDInstanceID                string // optional stable instance id for worker registration
	AppEnv                      string // "dev" | "test" | "staging" | "prod"
	ApiPort                     int
	WSPort                      int
	ReplayPort                  int
	ConsoleApiPort              int
	ConsoleWebappPort           int
	ProfileRPCPort              int
	ItemRPCPort                 int
	SortRPCPort                 int
	FeedRPCPort                 int
	AuthRPCPort                 int
	PMRPCPort                   int
	NotificationRPCPort         int
	TradeRPCPort                int
	LLMApiKey                   string
	LLMBaseURL                  string
	LLMModel                    string
	LLMTranslateModel           string // cheaper model for display translations; falls back to LLMModel when empty
	LLMMaxTokens                int
	LLMReasoningEffort          string
	EmbeddingProvider           string // "openai" or "ollama"
	EmbeddingApiKey             string
	EmbeddingBaseURL            string
	EmbeddingModel              string
	EmbeddingDimensions         int
	ResendApiKey                string
	ResendFromEmail             string
	EnableEmailVerification     bool     // Whether login requires OTP email verification
	MockUniversalOTP            string   // fixed OTP for whitelist-matched requests
	ESReplicas                  int      // Elasticsearch number_of_replicas
	ESShards                    int      // Elasticsearch number_of_shards
	EnableSearchCache           bool     // Enable search result caching
	SearchCacheTTL              int      // Search cache TTL in seconds (default: 2)
	ProfileCacheTTL             int      // Profile cache TTL in seconds (default: 60)
	MilestoneRuleCacheTTL       int      // Milestone rule cache TTL in seconds (default: 60)
	DisableDedupInTest          bool     // Disable deduplication in dev/test environments
	QualityThreshold            float64  // Quality score threshold for filtering items (default: 0.40)
	ItemConsumerWorkers         int      // Number of concurrent workers for item consumer (default: 10)
	FeedbackConsumerWorkers     int      // Number of concurrent workers for item stats consumer (default: 5)
	EmbeddingBackfillBatchSize  int      // Number of profiles per embedding backfill run (default: 200)
	EmbeddingBackfillInterval   string   // Embedding backfill cron interval (default: "5m")
	EmbeddingBackfillWorkers    int      // Concurrent workers for embedding backfill (default: 4)
	EmbeddingBackfillPauseMs    int      // Per-worker pause between embedding requests in milliseconds (default: 100)
	SuggestionBackfillBatchSize int      // Number of items per suggestion backfill run (default: 50)
	SuggestionBackfillInterval  string   // Suggestion backfill cron interval (default: "10m")
	SuggestionBackfillWorkers   int      // Concurrent workers for suggestion backfill (default: 2)
	SuggestionBackfillPauseMs   int      // Per-worker pause between LLM requests in milliseconds (default: 500)
	FreshnessOffset             string   // ES freshness decay offset, no decay within this duration (default: "12h")
	FreshnessScale              string   // ES freshness decay scale, time for score to decay to FreshnessDecay (default: "7d")
	FreshnessDecay              float64  // ES freshness decay factor at scale distance (default: 0.8)
	MockOTPEmailSuffixes        []string // Email suffixes that use mock OTP (e.g. ["@test.com"])
	MockOTPIPWhitelist          []string // IP whitelist for mock OTP
	MonitorEnabled              bool     // Enable distributed tracing (Jaeger) and log aggregation (Loki)
	OtelExporterEndpoint        string   // OTLP gRPC endpoint (default localhost:4317)
	LokiURL                     string   // Loki push API URL (default http://localhost:3122)
	LogLevel                    string   // Structured log level: debug | info | warn | error
	EnableReplayLog             bool     // Enable replay log publishing in FeedService (default: true)
	ReplayLogRetentionDays      int      // replay_logs rows older than this are purged by cron (default 30)
	ReplayLogCleanupIntervalSec int      // replay_logs cleanup cron interval (default 86400 = daily)

	// Official account (singleton new-user guide / first contact)
	OfficialAgentEmail          string   // email identifying the official account; resolved to agent_id at runtime
	OfficialAgentName           string   // display name for the official account
	OfficialAgentBio            string   // persona/bio for the official account
	OfficialWelcomeMessage      string   // welcome PM body sent to new users once their profile is complete
	EnableOfficialWelcome       bool     // master switch for the onboarding welcome (friend + PM) behavior
	OfficialWelcomeWhitelist    []string // when non-empty, only these emails receive the welcome (staged-rollout allowlist; empty = everyone)
	OfficialPMWhitelist         []string // staged-rollout allowlist for #4/#5 proactive official PMs (empty = all friends)
	EnableOfficialTrending      bool     // #5: biweekly network-wide trending DM
	EnableOfficialFeedRescue    bool     // #4: feed-deficit topic-recommendation DM
	OfficialTrendingIntervalSec int      // #5 cadence (default 14d)
	OfficialTrendingWindowDays  int      // #5 aggregation window (default 7, reuses the existing highlights/dashboard window)
	OfficialTrendingPoolN       int      // #5 top-N pool to sample from (default 20)
	OfficialTrendingPickN       int      // #5 topics per DM (default 3)
	OfficialRescueIntervalSec   int      // #4 cron cadence (default 1d)
	OfficialRescueWindowDays    int      // #4 feed lookback window (default 3)
	OfficialRescueThreshold     int      // #4 "insufficient" delivered-item count in window (default 30)
	OfficialRescueCooldownDays  int      // #4 per-user cooldown (default 3)
	OfficialLLMMaxPerRun        int      // cap on official LLM generations per cron run (rate guard, default 100)

	// Trade
	ChiefLedgerURL                string
	ChiefVerifyLookbackLimit      int // entries to scan per VerifyAgentTransfer call (default 50)
	ChiefHTTPTimeoutSec           int // per-request timeout for chief HTTP calls (default 10)
	TradeMaxActiveOrders          int
	TradeExpiryScanIntervalSec    int
	TradeOutboxDispatchIntervalMs int
	TradeOutboxCleanupIntervalSec int
	TradeOutboxRetentionDays      int
	TradeSearchSemanticWeight     float64
	TradeSearchKeywordWeight      float64
	TradeSearchSuccessWeight      float64
	TradeSearchLatencyWeight      float64
	TradeSearchPriceWeight        float64
	TradeSearchDeadlineWeight     float64

	// Score layer weights
	ScoreWeightSemantic  float64
	ScoreWeightKeyword   float64
	ScoreWeightFreshness float64
	ScoreWeightDiversity float64
	UrgencyBoost         float64
	UrgencyWindow        string
	MMRLambda            float64
	ExplorationSlots     int

	// Recall & ranking
	MinRelevanceScore        float64 // items below this total score are dropped from feed (default 0)
	KeywordRecallSize        int     // number of keyword recall candidates from ES (default 200)
	EnableKNNRecall          bool
	KNNRecallK               int
	KNNRecallCandidates      int
	EnableHotRecall          bool   // Enable hot_recall from Redis (default: true)
	EnableNewRecall          bool   // Enable new_recall from Redis (default: true)
	EnableTwoTowerRecall     bool   // Enable precomputed two_tower recall from Redis (default: false)
	EnableServiceMix         bool   // Mix trading services into the SortItems feed (default: false)
	ServiceMixRecallSize     int    // Max service candidates to recall before rerank (default: 50)
	RecallRedisNamespace     string // Redis key namespace for recall indices (default: "rec")
	TwoTowerRecallRedisKey   string // Redis recall output key for two_tower candidates (default: "two_tower_recall")
	TwoTowerRecallK          int    // Top-K for two-tower Redis candidates (default: 50)
	TwoTowerRecallCandidates int    // Deprecated; retained for env compatibility

	// Per-type freshness decay
	FreshnessAlertOffset  string
	FreshnessAlertScale   string
	FreshnessAlertDecay   float64
	FreshnessSupplyOffset string
	FreshnessSupplyScale  string
	FreshnessSupplyDecay  float64
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
		EtcdAddr:                      getEnv("ETCD_ADDR", "localhost:"+etcdPort),
		PgDSN:                         getEnv("PG_DSN", "postgres://eigenflux:eigenflux123@localhost:"+postgresPort+"/eigenflux?sslmode=disable"),
		RedisAddr:                     getEnv("REDIS_ADDR", "localhost:"+redisPort),
		RedisPassword:                 getEnv("REDIS_PASSWORD", ""),
		ProjectName:                   getEnv("PROJECT_NAME", defaultProjectName),
		ProjectTitle:                  getEnv("PROJECT_TITLE", defaultProjectTitle),
		PublicBaseURL:                 getEnv("PUBLIC_BASE_URL", ""),
		ESUsername:                    getEnv("ES_USERNAME", ""),
		ESPassword:                    getEnv("ES_PASSWORD", ""),
		IDWorkerPrefix:                getEnv("ID_WORKER_PREFIX", "/eigenflux/idgen/workers"),
		IDSnowflakeEpoch:              getEnvInt64("ID_SNOWFLAKE_EPOCH_MS", 1704067200000), // 2024-01-01 00:00:00 UTC
		IDWorkerLeaseTTL:              getEnvInt("ID_WORKER_LEASE_TTL", 30),
		IDInstanceID:                  getEnv("ID_INSTANCE_ID", ""),
		AppEnv:                        getEnv("APP_ENV", "dev"),
		ApiPort:                       getEnvInt("API_PORT", 8080),
		WSPort:                        getEnvInt("WS_PORT", 8088),
		ReplayPort:                    getEnvInt("REPLAY_PORT", 8092),
		ConsoleApiPort:                getEnvInt("CONSOLE_API_PORT", 8090),
		ConsoleWebappPort:             getEnvInt("CONSOLE_WEBAPP_PORT", 5173),
		ProfileRPCPort:                getEnvInt("PROFILE_RPC_PORT", 8881),
		ItemRPCPort:                   getEnvInt("ITEM_RPC_PORT", 8882),
		SortRPCPort:                   getEnvInt("SORT_RPC_PORT", 8883),
		FeedRPCPort:                   getEnvInt("FEED_RPC_PORT", 8884),
		PMRPCPort:                     getEnvInt("PM_RPC_PORT", 8885),
		AuthRPCPort:                   getEnvInt("AUTH_RPC_PORT", 8886),
		NotificationRPCPort:           getEnvInt("NOTIFICATION_RPC_PORT", 8887),
		TradeRPCPort:                  getEnvInt("TRADE_RPC_PORT", 8888),
		LLMApiKey:                     getEnv("LLM_API_KEY", ""),
		LLMBaseURL:                    getEnv("LLM_BASE_URL", "https://api.openai.com/v1"),
		LLMModel:                      getEnv("LLM_MODEL", "gpt-4o-mini"),
		LLMTranslateModel:             getEnv("LLM_TRANSLATE_MODEL", ""),
		LLMMaxTokens:                  getEnvInt("LLM_MAX_TOKENS", 4096),
		LLMReasoningEffort:            getEnv("LLM_REASONING_EFFORT", "low"),
		EmbeddingProvider:             embeddingProvider,
		EmbeddingApiKey:               getEnv("EMBEDDING_API_KEY", ""),
		EmbeddingBaseURL:              getEnv("EMBEDDING_BASE_URL", ""),
		EmbeddingModel:                embeddingModel,
		EmbeddingDimensions:           embeddingDimensions,
		ResendApiKey:                  getEnv("RESEND_API_KEY", ""),
		ResendFromEmail:               getEnv("RESEND_FROM_EMAIL", "noreply@example.com"),
		EnableEmailVerification:       getEnvBool("ENABLE_EMAIL_VERIFICATION", false),
		MockUniversalOTP:              getEnv("MOCK_UNIVERSAL_OTP", "123456"),
		ESReplicas:                    getEnvInt("ES_REPLICAS", 0),
		ESShards:                      getEnvInt("ES_SHARDS", 1),
		EnableSearchCache:             getEnvBool("ENABLE_SEARCH_CACHE", true),
		SearchCacheTTL:                getEnvInt("SEARCH_CACHE_TTL", 2),
		ProfileCacheTTL:               getEnvInt("PROFILE_CACHE_TTL", 60),
		MilestoneRuleCacheTTL:         getEnvInt("MILESTONE_RULE_CACHE_TTL", 60),
		DisableDedupInTest:            getEnvBool("DISABLE_DEDUP_IN_TEST", false),
		QualityThreshold:              getEnvFloat("QUALITY_THRESHOLD", 0.0),
		ItemConsumerWorkers:           getEnvInt("ITEM_CONSUMER_WORKERS", 10),
		FeedbackConsumerWorkers:       getEnvInt("FEEDBACK_CONSUMER_WORKERS", 5),
		EmbeddingBackfillBatchSize:    getEnvInt("EMBEDDING_BACKFILL_BATCH_SIZE", 200),
		EmbeddingBackfillInterval:     getEnv("EMBEDDING_BACKFILL_INTERVAL", "5m"),
		EmbeddingBackfillWorkers:      getEnvInt("EMBEDDING_BACKFILL_WORKERS", 4),
		EmbeddingBackfillPauseMs:      getEnvInt("EMBEDDING_BACKFILL_PAUSE_MS", 100),
		SuggestionBackfillBatchSize:   getEnvInt("SUGGESTION_BACKFILL_BATCH_SIZE", 50),
		SuggestionBackfillInterval:    getEnv("SUGGESTION_BACKFILL_INTERVAL", "10m"),
		SuggestionBackfillWorkers:     getEnvInt("SUGGESTION_BACKFILL_WORKERS", 2),
		SuggestionBackfillPauseMs:     getEnvInt("SUGGESTION_BACKFILL_PAUSE_MS", 500),
		FreshnessOffset:               getEnv("FRESHNESS_OFFSET", "12h"),
		FreshnessScale:                getEnv("FRESHNESS_SCALE", "7d"),
		FreshnessDecay:                getEnvFloat("FRESHNESS_DECAY", 0.8),
		MockOTPEmailSuffixes:          getEnvStringList("MOCK_OTP_EMAIL_SUFFIXES", nil),
		MockOTPIPWhitelist:            getEnvStringList("MOCK_OTP_IP_WHITELIST", nil),
		MonitorEnabled:                getEnvBool("MONITOR_ENABLED", false),
		OtelExporterEndpoint:          getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		LokiURL:                       getEnv("LOKI_URL", "http://localhost:3122"),
		LogLevel:                      getEnv("LOG_LEVEL", "debug"),
		EnableReplayLog:               getEnvBool("ENABLE_REPLAY_LOG", true),
		ReplayLogRetentionDays:        getEnvInt("REPLAY_LOG_RETENTION_DAYS", 30),
		ReplayLogCleanupIntervalSec:   getEnvInt("REPLAY_LOG_CLEANUP_INTERVAL_SEC", 86400),
		OfficialAgentEmail:            getEnv("OFFICIAL_AGENT_EMAIL", "eigenfluxofficial@gmail.com"),
		OfficialAgentName:             getEnv("OFFICIAL_AGENT_NAME", "eigenflux 官方助手"),
		OfficialAgentBio:              getEnv("OFFICIAL_AGENT_BIO", "你好，我是 Vic 老师，有什么可以帮助你的？"),
		OfficialWelcomeMessage:        getEnv("OFFICIAL_WELCOME_MESSAGE", "你好我是 vic 老师，我有什么可以帮助你的？"),
		EnableOfficialWelcome:         getEnvBool("ENABLE_OFFICIAL_WELCOME", true),
		OfficialWelcomeWhitelist:      getEnvStringList("OFFICIAL_WELCOME_WHITELIST", nil),
		OfficialPMWhitelist:           getEnvStringList("OFFICIAL_PM_WHITELIST", nil),
		EnableOfficialTrending:        getEnvBool("ENABLE_OFFICIAL_TRENDING", true),
		EnableOfficialFeedRescue:      getEnvBool("ENABLE_OFFICIAL_FEED_RESCUE", true),
		OfficialTrendingIntervalSec:   getEnvInt("OFFICIAL_TRENDING_INTERVAL_SEC", 14*86400),
		OfficialTrendingWindowDays:    getEnvInt("OFFICIAL_TRENDING_WINDOW_DAYS", 7),
		OfficialTrendingPoolN:         getEnvInt("OFFICIAL_TRENDING_POOL_N", 20),
		OfficialTrendingPickN:         getEnvInt("OFFICIAL_TRENDING_PICK_N", 3),
		OfficialRescueIntervalSec:     getEnvInt("OFFICIAL_RESCUE_INTERVAL_SEC", 86400),
		OfficialRescueWindowDays:      getEnvInt("OFFICIAL_RESCUE_WINDOW_DAYS", 3),
		OfficialRescueThreshold:       getEnvInt("OFFICIAL_RESCUE_THRESHOLD", 30),
		OfficialRescueCooldownDays:    getEnvInt("OFFICIAL_RESCUE_COOLDOWN_DAYS", 3),
		OfficialLLMMaxPerRun:          getEnvInt("OFFICIAL_LLM_MAX_PER_RUN", 100),
		ChiefLedgerURL:                getEnv("CHIEF_LEDGER_URL", "https://ledger.kovaloop.ai"),
		ChiefVerifyLookbackLimit:      getEnvInt("CHIEF_VERIFY_LOOKBACK_LIMIT", 50),
		ChiefHTTPTimeoutSec:           getEnvInt("CHIEF_HTTP_TIMEOUT_SEC", 10),
		TradeMaxActiveOrders:          getEnvInt("TRADE_MAX_ACTIVE_ORDERS", 3),
		TradeExpiryScanIntervalSec:    getEnvInt("TRADE_EXPIRY_SCAN_INTERVAL_SEC", 30),
		TradeOutboxDispatchIntervalMs: getEnvInt("TRADE_OUTBOX_DISPATCH_INTERVAL_MS", 1000),
		TradeOutboxCleanupIntervalSec: getEnvInt("TRADE_OUTBOX_CLEANUP_INTERVAL_SEC", 3600),
		TradeOutboxRetentionDays:      getEnvInt("TRADE_OUTBOX_RETENTION_DAYS", 7),
		TradeSearchSemanticWeight:     getEnvFloat("TRADE_SEARCH_SEMANTIC_WEIGHT", 0.55),
		TradeSearchKeywordWeight:      getEnvFloat("TRADE_SEARCH_KEYWORD_WEIGHT", 0.15),
		TradeSearchSuccessWeight:      getEnvFloat("TRADE_SEARCH_SUCCESS_WEIGHT", 0.15),
		TradeSearchLatencyWeight:      getEnvFloat("TRADE_SEARCH_LATENCY_WEIGHT", 0.07),
		TradeSearchPriceWeight:        getEnvFloat("TRADE_SEARCH_PRICE_WEIGHT", 0.05),
		TradeSearchDeadlineWeight:     getEnvFloat("TRADE_SEARCH_DEADLINE_WEIGHT", 0.03),
		ScoreWeightSemantic:           getEnvFloat("SCORE_WEIGHT_SEMANTIC", 0.4),
		ScoreWeightKeyword:            getEnvFloat("SCORE_WEIGHT_KEYWORD", 0.2),
		ScoreWeightFreshness:          getEnvFloat("SCORE_WEIGHT_FRESHNESS", 0.3),
		ScoreWeightDiversity:          getEnvFloat("SCORE_WEIGHT_DIVERSITY", 0.1),
		UrgencyBoost:                  getEnvFloat("URGENCY_BOOST", 0.5),
		UrgencyWindow:                 getEnv("URGENCY_WINDOW", "24h"),
		MMRLambda:                     getEnvFloat("MMR_LAMBDA", 0.7),
		ExplorationSlots:              getEnvInt("EXPLORATION_SLOTS", 0),
		MinRelevanceScore:             getEnvFloat("MIN_RELEVANCE_SCORE", 0.1),
		KeywordRecallSize:             getEnvInt("KEYWORD_RECALL_SIZE", 200),
		EnableKNNRecall:               getEnvBool("ENABLE_KNN_RECALL", true),
		KNNRecallK:                    getEnvInt("KNN_RECALL_K", 80),
		KNNRecallCandidates:           getEnvInt("KNN_RECALL_CANDIDATES", 300),
		EnableHotRecall:               getEnvBool("ENABLE_HOT_RECALL", true),
		EnableNewRecall:               getEnvBool("ENABLE_NEW_RECALL", true),
		EnableServiceMix:              getEnvBool("ENABLE_SERVICE_MIX", false),
		ServiceMixRecallSize:          getEnvInt("SERVICE_MIX_RECALL_SIZE", 50),
		EnableTwoTowerRecall:          getEnvBool("ENABLE_TWO_TOWER_RECALL", false),
		RecallRedisNamespace:          getEnv("REC_REDIS_NAMESPACE", "rec"),
		TwoTowerRecallRedisKey:        getEnv("TWO_TOWER_RECALL_REDIS_KEY", "two_tower_recall"),
		TwoTowerRecallK:               getEnvInt("TWO_TOWER_RECALL_K", 50),
		TwoTowerRecallCandidates:      getEnvInt("TWO_TOWER_RECALL_CANDIDATES", 200),
		FreshnessAlertOffset:          getEnv("FRESHNESS_ALERT_OFFSET", "2h"),
		FreshnessAlertScale:           getEnv("FRESHNESS_ALERT_SCALE", "12h"),
		FreshnessAlertDecay:           getEnvFloat("FRESHNESS_ALERT_DECAY", 0.5),
		FreshnessSupplyOffset:         getEnv("FRESHNESS_SUPPLY_OFFSET", "48h"),
		FreshnessSupplyScale:          getEnv("FRESHNESS_SUPPLY_SCALE", "30d"),
		FreshnessSupplyDecay:          getEnvFloat("FRESHNESS_SUPPLY_DECAY", 0.9),
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

// EffectiveLokiURL returns LokiURL when monitoring is enabled, empty otherwise.
func (c *Config) EffectiveLokiURL() string {
	if !c.MonitorEnabled {
		return ""
	}
	return c.LokiURL
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
