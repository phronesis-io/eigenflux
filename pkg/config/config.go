package config

import (
	"fmt"
	"os"
	"path"
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

type RegLimit struct {
	WindowSec   int
	IPLimit     int
	SubnetLimit int
	KeyLimit    int
	GlobalLimit int
}

func loadConsoleV2RegistrationLimits() RegLimit {
	return RegLimit{
		WindowSec:   getEnvInt("CONSOLE_V2_REGISTRATION_WINDOW_SEC", 86400),
		IPLimit:     getEnvInt("CONSOLE_V2_REGISTRATION_IP_LIMIT", 500),
		SubnetLimit: getEnvInt("CONSOLE_V2_REGISTRATION_SUBNET_LIMIT", 500),
		KeyLimit:    getEnvInt("CONSOLE_V2_REGISTRATION_KEY_LIMIT", 5),
		GlobalLimit: getEnvInt("CONSOLE_V2_REGISTRATION_GLOBAL_LIMIT", 1000),
	}
}

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
	LLMApiKey                   string
	LLMBaseURL                  string
	LLMModel                    string
	LLMTranslateModel           string // cheaper model for display translations; falls back to LLMModel when empty
	LLMMaxTokens                int
	LLMReasoningEffort          string
	SafetyLLMApiKey             string // Volcengine Ark key for the safety filter; falls back to LLMApiKey when empty
	SafetyLLMBaseURL            string // Volcengine Ark base URL for the safety filter; used verbatim (no /v1 suffixing)
	SafetyLLMModel              string // Volcengine Ark model name or endpoint ID for the safety filter
	EmbeddingProvider           string // "openai" or "ollama"
	EmbeddingApiKey             string
	EmbeddingBaseURL            string
	EmbeddingModel              string
	EmbeddingDimensions         int
	ResendApiKey                string
	ResendFromEmail             string
	EnableEmailVerification     bool     // Whether login requires OTP email verification
	EnableConsoleV2             bool     // Enable the isolated Console V2 BFF and onboarding routes
	EnableFeedV2                bool     // Enable the stateless latest-view Feed V2 route
	EnableControlChannelV2      bool     // Enable Agent command routes
	EnableAgentAttentionV1      bool     // Enable only agent_attention.v1 publish and Console routes
	EnableCommunicationV2       bool     // Enable V2 PM/friend responses enriched with public Agent Card data
	EnablePublicRegistration    bool     // Allow Agents to obtain a rate-limited key-bound bootstrap challenge without a broker
	ConsoleV2BootstrapSecret    string   // Shared secret used only by the controlled bootstrap broker
	ConsoleV2OTPPepper          string   // Server-side HMAC pepper for Console V2 email challenges
	ConsoleV2PublicURL          string   // Browser origin used when constructing one-time handoff URLs
	ConsoleV2TrustedProxyCIDRs  []string // Proxies allowed to supply client IP forwarding headers for V2 OTP limits
	ConsoleV2Registration       RegLimit // Public automatic registration rate limits
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
	MqStreamMaxLen              int64    // approximate cap on Redis Stream length applied by mq.Publish (default 20000, <=0 disables); ingestion streams are exempt

	// Official account (singleton new-user guide / first contact)
	OfficialAgentEmail           string   // email identifying the official account; resolved to agent_id at runtime
	OfficialAgentName            string   // display name for the official account
	OfficialAgentBio             string   // persona/bio for the official account
	OfficialWelcomeMessage       string   // welcome PM body sent to new users once their profile is complete
	EnableOfficialWelcome        bool     // master switch for the onboarding welcome (friend + PM) behavior
	OfficialTestEmailSuffixes    []string // test-account matchers for the login test-OTP path: "@domain" entries match by suffix, full-address patterns support glob syntax; empty (default) disables
	OfficialTestOTP              string   // fixed login OTP for test-account emails (matching OfficialTestEmailSuffixes); empty (default) disables the test-login path
	EnableOfficialTrending       bool     // #5: biweekly network-wide trending DM
	EnableOfficialFeedRescue     bool     // #4: feed-deficit topic-recommendation DM
	OfficialTrendingIntervalSec  int      // #5 cadence (default 14d)
	OfficialTrendingWindowDays   int      // #5 aggregation window (default 7, reuses the existing highlights/dashboard window)
	OfficialTrendingPoolN        int      // #5 top-N pool to sample from (default 20)
	OfficialTrendingPickN        int      // #5 topics per DM (default 3)
	OfficialRescueIntervalSec    int      // #4 cron cadence (default 1d)
	OfficialRescueWindowDays     int      // #4 feed lookback window (default 3)
	OfficialRescueThreshold      int      // #4 "insufficient" delivered-item count in window (default 30)
	OfficialRescueCooldownDays   int      // #4 per-user cooldown (default 3)
	OfficialLLMMaxPerRun         int      // cap on official LLM generations per cron run (rate guard, default 100)
	OfficialChatDailyPerUser     int      // #2/#3: max official LLM replies per user per day (default 50)
	OfficialChatPerUserPerMin    int      // #2/#3: max official LLM replies per user per minute (default 1)
	OfficialChatGlobalPerMin     int      // #2/#3: global cap on official LLM replies per minute (default 60)
	EnableOfficialChat           bool     // #2: official replies to friend DMs (inbox consumer)
	EnableOfficialFirstBroadcast bool     // #3: official replies to a new member's first broadcast

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
	MinRelevanceScore     float64 // items below this total score are dropped from feed (default 0)
	FriendFeedEnabled     bool    // inject friends' recent broadcasts into the feed, bypassing the relevance threshold (best-effort, not guaranteed)
	FriendFeedWindowHours int     // how far back to pull friends' broadcasts
	FriendFeedMaxItems    int     // cap friend items injected per feed fetch
	FriendFeedMaxAuthors  int     // cap friends queried per feed fetch
	KeywordRecallSize     int     // number of keyword recall candidates from ES (default 200)
	EnableKNNRecall       bool
	KNNRecallK            int
	KNNRecallCandidates   int
	EnableHotRecall       bool     // Enable hot_recall from Redis (default: true)
	EnableNewRecall       bool     // Enable new_recall from Redis (default: true)
	EnableNewUGCRecall    bool     // Enable new_ugc recall from Redis: un-exposed UGC candidates written by the offline service. Force-insertion is configured in configs/sort/rerank.yaml (name: inject) (default: false)
	EnableSwingI2IRecall  bool     // Enable Swing item-to-item recall from the offline Redis index (default: false)
	SwingI2IRecallSeeds   int      // Maximum impressed seed items expanded per request (default: 20)
	SwingI2IRecallK       int      // Maximum aggregated Swing candidates returned per request (default: 100)
	PGCEmailSuffixes      []string // author email suffixes classifying a broadcast as PGC (official bots); everything else is UGC. Drives the sort UGC boost and category metrics
	BlockedAgentEmails    []string // agent emails denied at the API auth gate (spam/abuse); blocks every authenticated route including broadcast publish
	RecallRedisNamespace  string   // Redis key namespace for recall indices (default: "rec")

	// LR ranker (sort). A daily-trained logistic-regression model replaces the
	// formula rank when enabled and a valid bundle is loaded; otherwise sort
	// falls back to the baseline formula ranker. The bundle is delivered to a
	// local directory out-of-band (OSS sync + atomic `current` symlink); sort
	// only hot-reloads the local file.
	LRRankerEnabled        bool
	LRRankerModelPath      string
	LRRankerReloadInterval string

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
		EtcdAddr:                     getEnv("ETCD_ADDR", "localhost:"+etcdPort),
		PgDSN:                        getEnv("PG_DSN", "postgres://eigenflux:eigenflux123@localhost:"+postgresPort+"/eigenflux?sslmode=disable"),
		RedisAddr:                    getEnv("REDIS_ADDR", "localhost:"+redisPort),
		RedisPassword:                getEnv("REDIS_PASSWORD", ""),
		ProjectName:                  getEnv("PROJECT_NAME", defaultProjectName),
		ProjectTitle:                 getEnv("PROJECT_TITLE", defaultProjectTitle),
		PublicBaseURL:                getEnv("PUBLIC_BASE_URL", ""),
		ESUsername:                   getEnv("ES_USERNAME", ""),
		ESPassword:                   getEnv("ES_PASSWORD", ""),
		IDWorkerPrefix:               getEnv("ID_WORKER_PREFIX", "/eigenflux/idgen/workers"),
		IDSnowflakeEpoch:             getEnvInt64("ID_SNOWFLAKE_EPOCH_MS", 1704067200000), // 2024-01-01 00:00:00 UTC
		IDWorkerLeaseTTL:             getEnvInt("ID_WORKER_LEASE_TTL", 30),
		IDInstanceID:                 getEnv("ID_INSTANCE_ID", ""),
		AppEnv:                       getEnv("APP_ENV", "dev"),
		ApiPort:                      getEnvInt("API_PORT", 8080),
		WSPort:                       getEnvInt("WS_PORT", 8088),
		ReplayPort:                   getEnvInt("REPLAY_PORT", 8092),
		ConsoleApiPort:               getEnvInt("CONSOLE_API_PORT", 8090),
		ConsoleWebappPort:            getEnvInt("CONSOLE_WEBAPP_PORT", 5173),
		ProfileRPCPort:               getEnvInt("PROFILE_RPC_PORT", 8881),
		ItemRPCPort:                  getEnvInt("ITEM_RPC_PORT", 8882),
		SortRPCPort:                  getEnvInt("SORT_RPC_PORT", 8883),
		FeedRPCPort:                  getEnvInt("FEED_RPC_PORT", 8884),
		PMRPCPort:                    getEnvInt("PM_RPC_PORT", 8885),
		AuthRPCPort:                  getEnvInt("AUTH_RPC_PORT", 8886),
		NotificationRPCPort:          getEnvInt("NOTIFICATION_RPC_PORT", 8887),
		LLMApiKey:                    getEnv("LLM_API_KEY", ""),
		LLMBaseURL:                   getEnv("LLM_BASE_URL", "https://api.openai.com/v1"),
		LLMModel:                     getEnv("LLM_MODEL", "gpt-4o-mini"),
		LLMTranslateModel:            getEnv("LLM_TRANSLATE_MODEL", ""),
		LLMMaxTokens:                 getEnvInt("LLM_MAX_TOKENS", 4096),
		LLMReasoningEffort:           getEnv("LLM_REASONING_EFFORT", "low"),
		SafetyLLMApiKey:              getEnv("SAFETY_LLM_API_KEY", ""),
		SafetyLLMBaseURL:             getEnv("SAFETY_LLM_BASE_URL", "https://ark.cn-beijing.volces.com/api/v3"),
		SafetyLLMModel:               getEnv("SAFETY_LLM_MODEL", ""),
		EmbeddingProvider:            embeddingProvider,
		EmbeddingApiKey:              getEnv("EMBEDDING_API_KEY", ""),
		EmbeddingBaseURL:             getEnv("EMBEDDING_BASE_URL", ""),
		EmbeddingModel:               embeddingModel,
		EmbeddingDimensions:          embeddingDimensions,
		ResendApiKey:                 getEnv("RESEND_API_KEY", ""),
		ResendFromEmail:              getEnv("RESEND_FROM_EMAIL", "noreply@example.com"),
		EnableEmailVerification:      getEnvBool("ENABLE_EMAIL_VERIFICATION", false),
		EnableConsoleV2:              getEnvBool("ENABLE_CONSOLE_V2", false),
		EnableFeedV2:                 getEnvBool("ENABLE_FEED_V2", false),
		EnableControlChannelV2:       getEnvBool("ENABLE_CONTROL_CHANNEL_V2", false),
		EnableAgentAttentionV1:       getEnvBool("ENABLE_AGENT_ATTENTION_V1", false),
		EnableCommunicationV2:        getEnvBool("ENABLE_COMMUNICATION_V2", false),
		EnablePublicRegistration:     getEnvBool("ENABLE_PUBLIC_AGENT_REGISTRATION", false),
		ConsoleV2BootstrapSecret:     getEnv("CONSOLE_V2_BOOTSTRAP_SECRET", ""),
		ConsoleV2OTPPepper:           getEnv("CONSOLE_V2_OTP_PEPPER", ""),
		ConsoleV2PublicURL:           getEnv("CONSOLE_V2_PUBLIC_URL", "http://localhost:5173"),
		ConsoleV2TrustedProxyCIDRs:   getEnvStringList("CONSOLE_V2_TRUSTED_PROXY_CIDRS", nil),
		ConsoleV2Registration:        loadConsoleV2RegistrationLimits(),
		MockUniversalOTP:             getEnv("MOCK_UNIVERSAL_OTP", "123456"),
		ESReplicas:                   getEnvInt("ES_REPLICAS", 0),
		ESShards:                     getEnvInt("ES_SHARDS", 1),
		EnableSearchCache:            getEnvBool("ENABLE_SEARCH_CACHE", true),
		SearchCacheTTL:               getEnvInt("SEARCH_CACHE_TTL", 2),
		ProfileCacheTTL:              getEnvInt("PROFILE_CACHE_TTL", 60),
		MilestoneRuleCacheTTL:        getEnvInt("MILESTONE_RULE_CACHE_TTL", 60),
		DisableDedupInTest:           getEnvBool("DISABLE_DEDUP_IN_TEST", false),
		QualityThreshold:             getEnvFloat("QUALITY_THRESHOLD", 0.0),
		ItemConsumerWorkers:          getEnvInt("ITEM_CONSUMER_WORKERS", 10),
		FeedbackConsumerWorkers:      getEnvInt("FEEDBACK_CONSUMER_WORKERS", 5),
		EmbeddingBackfillBatchSize:   getEnvInt("EMBEDDING_BACKFILL_BATCH_SIZE", 200),
		EmbeddingBackfillInterval:    getEnv("EMBEDDING_BACKFILL_INTERVAL", "5m"),
		EmbeddingBackfillWorkers:     getEnvInt("EMBEDDING_BACKFILL_WORKERS", 4),
		EmbeddingBackfillPauseMs:     getEnvInt("EMBEDDING_BACKFILL_PAUSE_MS", 100),
		SuggestionBackfillBatchSize:  getEnvInt("SUGGESTION_BACKFILL_BATCH_SIZE", 50),
		SuggestionBackfillInterval:   getEnv("SUGGESTION_BACKFILL_INTERVAL", "10m"),
		SuggestionBackfillWorkers:    getEnvInt("SUGGESTION_BACKFILL_WORKERS", 2),
		SuggestionBackfillPauseMs:    getEnvInt("SUGGESTION_BACKFILL_PAUSE_MS", 500),
		FreshnessOffset:              getEnv("FRESHNESS_OFFSET", "12h"),
		FreshnessScale:               getEnv("FRESHNESS_SCALE", "7d"),
		FreshnessDecay:               getEnvFloat("FRESHNESS_DECAY", 0.8),
		MockOTPEmailSuffixes:         getEnvStringList("MOCK_OTP_EMAIL_SUFFIXES", nil),
		MockOTPIPWhitelist:           getEnvStringList("MOCK_OTP_IP_WHITELIST", nil),
		MonitorEnabled:               getEnvBool("MONITOR_ENABLED", false),
		OtelExporterEndpoint:         getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		LokiURL:                      getEnv("LOKI_URL", "http://localhost:3122"),
		LogLevel:                     getEnv("LOG_LEVEL", "debug"),
		EnableReplayLog:              getEnvBool("ENABLE_REPLAY_LOG", true),
		ReplayLogRetentionDays:       getEnvInt("REPLAY_LOG_RETENTION_DAYS", 30),
		ReplayLogCleanupIntervalSec:  getEnvInt("REPLAY_LOG_CLEANUP_INTERVAL_SEC", 86400),
		MqStreamMaxLen:               getEnvInt64("MQ_STREAM_MAXLEN", 20000),
		OfficialAgentEmail:           getEnv("OFFICIAL_AGENT_EMAIL", "eigenfluxofficial@gmail.com"),
		OfficialAgentName:            getEnv("OFFICIAL_AGENT_NAME", "eigenflux 官方助手"),
		OfficialAgentBio:             getEnv("OFFICIAL_AGENT_BIO", "你好，我是 Vic 老师，有什么可以帮助你的？"),
		OfficialWelcomeMessage:       getEnv("OFFICIAL_WELCOME_MESSAGE", "你好我是 vic 老师，我有什么可以帮助你的？"),
		EnableOfficialWelcome:        getEnvBool("ENABLE_OFFICIAL_WELCOME", true),
		OfficialTestEmailSuffixes:    getEnvStringList("OFFICIAL_TEST_EMAIL_SUFFIXES", nil),
		OfficialTestOTP:              getEnv("OFFICIAL_TEST_OTP", ""),
		EnableOfficialTrending:       getEnvBool("ENABLE_OFFICIAL_TRENDING", true),
		EnableOfficialFeedRescue:     getEnvBool("ENABLE_OFFICIAL_FEED_RESCUE", true),
		OfficialTrendingIntervalSec:  getEnvInt("OFFICIAL_TRENDING_INTERVAL_SEC", 14*86400),
		OfficialTrendingWindowDays:   getEnvInt("OFFICIAL_TRENDING_WINDOW_DAYS", 7),
		OfficialTrendingPoolN:        getEnvInt("OFFICIAL_TRENDING_POOL_N", 20),
		OfficialTrendingPickN:        getEnvInt("OFFICIAL_TRENDING_PICK_N", 3),
		OfficialRescueIntervalSec:    getEnvInt("OFFICIAL_RESCUE_INTERVAL_SEC", 86400),
		OfficialRescueWindowDays:     getEnvInt("OFFICIAL_RESCUE_WINDOW_DAYS", 3),
		OfficialRescueThreshold:      getEnvInt("OFFICIAL_RESCUE_THRESHOLD", 30),
		OfficialRescueCooldownDays:   getEnvInt("OFFICIAL_RESCUE_COOLDOWN_DAYS", 3),
		OfficialLLMMaxPerRun:         getEnvInt("OFFICIAL_LLM_MAX_PER_RUN", 100),
		OfficialChatDailyPerUser:     getEnvInt("OFFICIAL_CHAT_DAILY_PER_USER", 50),
		OfficialChatPerUserPerMin:    getEnvInt("OFFICIAL_CHAT_PER_USER_PER_MIN", 1),
		OfficialChatGlobalPerMin:     getEnvInt("OFFICIAL_CHAT_GLOBAL_PER_MIN", 60),
		EnableOfficialChat:           getEnvBool("ENABLE_OFFICIAL_CHAT", true),
		EnableOfficialFirstBroadcast: getEnvBool("ENABLE_OFFICIAL_FIRST_BROADCAST", true),
		ScoreWeightSemantic:          getEnvFloat("SCORE_WEIGHT_SEMANTIC", 0.4),
		ScoreWeightKeyword:           getEnvFloat("SCORE_WEIGHT_KEYWORD", 0.2),
		ScoreWeightFreshness:         getEnvFloat("SCORE_WEIGHT_FRESHNESS", 0.3),
		ScoreWeightDiversity:         getEnvFloat("SCORE_WEIGHT_DIVERSITY", 0.1),
		UrgencyBoost:                 getEnvFloat("URGENCY_BOOST", 0.5),
		UrgencyWindow:                getEnv("URGENCY_WINDOW", "24h"),
		MMRLambda:                    getEnvFloat("MMR_LAMBDA", 0.7),
		ExplorationSlots:             getEnvInt("EXPLORATION_SLOTS", 0),
		MinRelevanceScore:            getEnvFloat("MIN_RELEVANCE_SCORE", 0.1),
		FriendFeedEnabled:            getEnvBool("FRIEND_FEED_ENABLED", true),
		FriendFeedWindowHours:        getEnvInt("FRIEND_FEED_WINDOW_HOURS", 168),
		FriendFeedMaxItems:           getEnvInt("FRIEND_FEED_MAX_ITEMS", 20),
		FriendFeedMaxAuthors:         getEnvInt("FRIEND_FEED_MAX_AUTHORS", 50),
		KeywordRecallSize:            getEnvInt("KEYWORD_RECALL_SIZE", 200),
		EnableKNNRecall:              getEnvBool("ENABLE_KNN_RECALL", false),
		KNNRecallK:                   getEnvInt("KNN_RECALL_K", 80),
		KNNRecallCandidates:          getEnvInt("KNN_RECALL_CANDIDATES", 300),
		EnableHotRecall:              getEnvBool("ENABLE_HOT_RECALL", true),
		EnableNewRecall:              getEnvBool("ENABLE_NEW_RECALL", true),
		EnableNewUGCRecall:           getEnvBool("ENABLE_NEW_UGC_RECALL", false),
		EnableSwingI2IRecall:         getEnvBool("ENABLE_SWING_I2I_RECALL", false),
		SwingI2IRecallSeeds:          getEnvInt("SWING_I2I_RECALL_SEEDS", 20),
		SwingI2IRecallK:              getEnvInt("SWING_I2I_RECALL_K", 100),
		PGCEmailSuffixes:             getEnvStringList("PGC_EMAIL_SUFFIXES", []string{"@bot.eigenflux.one", "@pgc.eigenflux.one"}),
		BlockedAgentEmails:           getEnvStringList("BLOCKED_AGENT_EMAILS", []string{"fmw19990718@gmail.com"}),
		LRRankerEnabled:              getEnvBool("LR_RANKER_ENABLED", false),
		LRRankerModelPath:            getEnv("LR_RANKER_MODEL_PATH", "/data/models/eigenflux/lr-ranker/current/model.json"),
		LRRankerReloadInterval:       getEnv("LR_RANKER_RELOAD_INTERVAL", "60s"),
		RecallRedisNamespace:         getEnv("REC_REDIS_NAMESPACE", "rec"),
		FreshnessAlertOffset:         getEnv("FRESHNESS_ALERT_OFFSET", "2h"),
		FreshnessAlertScale:          getEnv("FRESHNESS_ALERT_SCALE", "12h"),
		FreshnessAlertDecay:          getEnvFloat("FRESHNESS_ALERT_DECAY", 0.5),
		FreshnessSupplyOffset:        getEnv("FRESHNESS_SUPPLY_OFFSET", "48h"),
		FreshnessSupplyScale:         getEnv("FRESHNESS_SUPPLY_SCALE", "30d"),
		FreshnessSupplyDecay:         getEnvFloat("FRESHNESS_SUPPLY_DECAY", 0.9),
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
// EmailMatchesAnySuffix reports whether email matches any of the given
// entries (case-insensitive). Entries starting with "@" match by suffix;
// all other entries must match the full address exactly.
func EmailMatchesAnySuffix(email string, suffixes []string) bool {
	e := strings.ToLower(strings.TrimSpace(email))
	if e == "" {
		return false
	}
	for _, s := range suffixes {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if strings.HasPrefix(s, "@") {
			if strings.HasSuffix(e, s) {
				return true
			}
			continue
		}
		if e == s {
			return true
		}
	}
	return false
}

// EmailMatchesAnyPattern reports whether email matches any configured test
// account pattern (case-insensitive). Entries starting with "@" retain the
// domain-suffix behavior. Other entries are matched against the full address
// using path.Match syntax, including *, ?, and character classes such as
// [0-9]. Invalid patterns fail closed.
func EmailMatchesAnyPattern(email string, patterns []string) bool {
	e := strings.ToLower(strings.TrimSpace(email))
	if e == "" {
		return false
	}
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if strings.HasPrefix(pattern, "@") {
			if strings.HasSuffix(e, pattern) {
				return true
			}
			continue
		}
		matched, err := path.Match(pattern, e)
		if err == nil && matched {
			return true
		}
	}
	return false
}

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
