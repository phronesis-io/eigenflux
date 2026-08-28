package config

import "testing"

func TestIsProdEnv(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		env    string
		expect bool
	}{
		{name: "prod", env: "prod", expect: true},
		{name: "production", env: "production", expect: true},
		{name: "upper-case", env: "PRODUCTION", expect: true},
		{name: "test", env: "test", expect: false},
		{name: "dev", env: "dev", expect: false},
		{name: "empty", env: "", expect: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := IsProdEnv(tc.env)
			if got != tc.expect {
				t.Fatalf("IsProdEnv(%q)=%v, want %v", tc.env, got, tc.expect)
			}
		})
	}
}

func TestShouldDisableDedup(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		cfg    *Config
		expect bool
	}{
		{
			name: "test-enabled",
			cfg: &Config{
				AppEnv:             "test",
				DisableDedupInTest: true,
			},
			expect: true,
		},
		{
			name: "test-disabled",
			cfg: &Config{
				AppEnv:             "test",
				DisableDedupInTest: false,
			},
			expect: false,
		},
		{
			name: "dev-enabled",
			cfg: &Config{
				AppEnv:             "dev",
				DisableDedupInTest: true,
			},
			expect: true,
		},
		{
			name: "dev-disabled",
			cfg: &Config{
				AppEnv:             "dev",
				DisableDedupInTest: false,
			},
			expect: false,
		},
		{
			name: "prod-ignored-even-when-enabled",
			cfg: &Config{
				AppEnv:             "prod",
				DisableDedupInTest: true,
			},
			expect: false,
		},
		{
			name: "production-ignored-even-when-enabled",
			cfg: &Config{
				AppEnv:             "production",
				DisableDedupInTest: true,
			},
			expect: false,
		},
		{
			name:   "nil-config",
			cfg:    nil,
			expect: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.cfg.ShouldDisableDedup()
			if got != tc.expect {
				t.Fatalf("ShouldDisableDedup()=%v, want %v", got, tc.expect)
			}
		})
	}
}

func TestLoadMilestoneRuleCacheTTL(t *testing.T) {
	t.Setenv("MILESTONE_RULE_CACHE_TTL", "5")
	t.Setenv("POSTGRES_PORT", "")
	t.Setenv("REDIS_PORT", "")
	t.Setenv("ETCD_PORT", "")

	cfg := Load()
	if cfg.MilestoneRuleCacheTTL != 5 {
		t.Fatalf("MilestoneRuleCacheTTL=%d, want 5", cfg.MilestoneRuleCacheTTL)
	}
}

func TestLoadMilestoneRuleCacheTTLDefault(t *testing.T) {
	t.Setenv("MILESTONE_RULE_CACHE_TTL", "")
	t.Setenv("POSTGRES_PORT", "")
	t.Setenv("REDIS_PORT", "")
	t.Setenv("ETCD_PORT", "")

	cfg := Load()
	if cfg.MilestoneRuleCacheTTL != 60 {
		t.Fatalf("MilestoneRuleCacheTTL=%d, want 60", cfg.MilestoneRuleCacheTTL)
	}
}

func TestConsoleV2FeatureFlagsDefaultOff(t *testing.T) {
	t.Setenv("ENABLE_CONSOLE_V2", "")
	t.Setenv("ENABLE_FEED_V2", "")
	t.Setenv("ENABLE_CONTROL_CHANNEL_V2", "")
	t.Setenv("ENABLE_AGENT_ATTENTION_V1", "")
	t.Setenv("ENABLE_COMMUNICATION_V2", "")
	t.Setenv("ENABLE_PUBLIC_AGENT_REGISTRATION", "")
	t.Setenv("POSTGRES_PORT", "")
	t.Setenv("REDIS_PORT", "")
	t.Setenv("ETCD_PORT", "")

	cfg := Load()
	if cfg.EnableConsoleV2 || cfg.EnableFeedV2 || cfg.EnableControlChannelV2 || cfg.EnableAgentAttentionV1 || cfg.EnableCommunicationV2 || cfg.EnablePublicRegistration {
		t.Fatal("Console V2 feature flags must default to disabled")
	}
	if cfg.ConsoleV2Registration.WindowSec != 86400 || cfg.ConsoleV2Registration.IPLimit != 500 ||
		cfg.ConsoleV2Registration.SubnetLimit != 500 || cfg.ConsoleV2Registration.KeyLimit != 5 ||
		cfg.ConsoleV2Registration.GlobalLimit != 1000 {
		t.Fatalf("unexpected public registration defaults: %#v", cfg.ConsoleV2Registration)
	}
}

func TestConsoleV2FeatureFlagsCanBeEnabledIndependently(t *testing.T) {
	t.Setenv("ENABLE_CONSOLE_V2", "true")
	t.Setenv("ENABLE_FEED_V2", "false")
	t.Setenv("ENABLE_CONTROL_CHANNEL_V2", "true")
	t.Setenv("ENABLE_AGENT_ATTENTION_V1", "true")
	t.Setenv("ENABLE_COMMUNICATION_V2", "false")
	t.Setenv("ENABLE_PUBLIC_AGENT_REGISTRATION", "true")
	t.Setenv("POSTGRES_PORT", "")
	t.Setenv("REDIS_PORT", "")
	t.Setenv("ETCD_PORT", "")

	cfg := Load()
	if !cfg.EnableConsoleV2 || cfg.EnableFeedV2 || !cfg.EnableControlChannelV2 || !cfg.EnableAgentAttentionV1 || cfg.EnableCommunicationV2 || !cfg.EnablePublicRegistration {
		t.Fatal("Console V2 feature flags were not loaded independently")
	}
}

func TestLoadRedisPassword(t *testing.T) {
	t.Setenv("REDIS_PASSWORD", "secret-redis-password")
	t.Setenv("POSTGRES_PORT", "")
	t.Setenv("REDIS_PORT", "")
	t.Setenv("ETCD_PORT", "")

	cfg := Load()
	if cfg.RedisPassword != "secret-redis-password" {
		t.Fatalf("RedisPassword=%q, want %q", cfg.RedisPassword, "secret-redis-password")
	}
}

func TestLoadESCredentials(t *testing.T) {
	t.Setenv("ES_USERNAME", "elastic")
	t.Setenv("ES_PASSWORD", "secret-es-password")
	t.Setenv("POSTGRES_PORT", "")
	t.Setenv("REDIS_PORT", "")
	t.Setenv("ETCD_PORT", "")

	cfg := Load()
	if cfg.ESUsername != "elastic" {
		t.Fatalf("ESUsername=%q, want %q", cfg.ESUsername, "elastic")
	}
	if cfg.ESPassword != "secret-es-password" {
		t.Fatalf("ESPassword=%q, want %q", cfg.ESPassword, "secret-es-password")
	}
}

func TestLoadLLMDefaults(t *testing.T) {
	t.Setenv("LLM_BASE_URL", "")
	t.Setenv("LLM_MODEL", "")
	t.Setenv("POSTGRES_PORT", "")
	t.Setenv("REDIS_PORT", "")
	t.Setenv("ETCD_PORT", "")

	cfg := Load()
	if cfg.LLMBaseURL != "https://api.openai.com/v1" {
		t.Fatalf("LLMBaseURL=%q, want %q", cfg.LLMBaseURL, "https://api.openai.com/v1")
	}
	if cfg.LLMModel != "gpt-4o-mini" {
		t.Fatalf("LLMModel=%q, want %q", cfg.LLMModel, "gpt-4o-mini")
	}
}

func TestLoadEmbeddingBackfillDefaults(t *testing.T) {
	t.Setenv("EMBEDDING_BACKFILL_BATCH_SIZE", "")
	t.Setenv("EMBEDDING_BACKFILL_INTERVAL", "")
	t.Setenv("EMBEDDING_BACKFILL_WORKERS", "")
	t.Setenv("EMBEDDING_BACKFILL_PAUSE_MS", "")
	t.Setenv("POSTGRES_PORT", "")
	t.Setenv("REDIS_PORT", "")
	t.Setenv("ETCD_PORT", "")

	cfg := Load()
	if cfg.EmbeddingBackfillBatchSize != 200 {
		t.Fatalf("EmbeddingBackfillBatchSize=%d, want 200", cfg.EmbeddingBackfillBatchSize)
	}
	if cfg.EmbeddingBackfillInterval != "5m" {
		t.Fatalf("EmbeddingBackfillInterval=%q, want %q", cfg.EmbeddingBackfillInterval, "5m")
	}
	if cfg.EmbeddingBackfillWorkers != 4 {
		t.Fatalf("EmbeddingBackfillWorkers=%d, want 4", cfg.EmbeddingBackfillWorkers)
	}
	if cfg.EmbeddingBackfillPauseMs != 100 {
		t.Fatalf("EmbeddingBackfillPauseMs=%d, want 100", cfg.EmbeddingBackfillPauseMs)
	}
}

func TestLoadEmbeddingBackfillOverrides(t *testing.T) {
	t.Setenv("EMBEDDING_BACKFILL_BATCH_SIZE", "300")
	t.Setenv("EMBEDDING_BACKFILL_INTERVAL", "3m")
	t.Setenv("EMBEDDING_BACKFILL_WORKERS", "5")
	t.Setenv("EMBEDDING_BACKFILL_PAUSE_MS", "50")
	t.Setenv("POSTGRES_PORT", "")
	t.Setenv("REDIS_PORT", "")
	t.Setenv("ETCD_PORT", "")

	cfg := Load()
	if cfg.EmbeddingBackfillBatchSize != 300 {
		t.Fatalf("EmbeddingBackfillBatchSize=%d, want 300", cfg.EmbeddingBackfillBatchSize)
	}
	if cfg.EmbeddingBackfillInterval != "3m" {
		t.Fatalf("EmbeddingBackfillInterval=%q, want %q", cfg.EmbeddingBackfillInterval, "3m")
	}
	if cfg.EmbeddingBackfillWorkers != 5 {
		t.Fatalf("EmbeddingBackfillWorkers=%d, want 5", cfg.EmbeddingBackfillWorkers)
	}
	if cfg.EmbeddingBackfillPauseMs != 50 {
		t.Fatalf("EmbeddingBackfillPauseMs=%d, want 50", cfg.EmbeddingBackfillPauseMs)
	}
}

func TestLoadCommissionIntegrationFields(t *testing.T) {
	t.Setenv("COMMISSION_INTEGRATION_MODE", "true")
	t.Setenv("INTEGRATION_CONTROL_ADDR", "127.0.0.1:18081")
	t.Setenv("INTEGRATION_CONTROL_TOKEN", "0123456789abcdef0123456789abcdef")

	cfg := Load()
	if cfg.CommissionIntegrationFlag != "true" {
		t.Fatalf("CommissionIntegrationFlag=%q, want true", cfg.CommissionIntegrationFlag)
	}
	if cfg.IntegrationControlAddr != "127.0.0.1:18081" {
		t.Fatalf("IntegrationControlAddr=%q", cfg.IntegrationControlAddr)
	}
	if cfg.IntegrationControlToken != "0123456789abcdef0123456789abcdef" {
		t.Fatal("IntegrationControlToken was not loaded")
	}
}

func TestCommissionIntegrationModeDefaultsDisabled(t *testing.T) {
	mode, err := (&Config{}).CommissionIntegrationMode()
	if err != nil {
		t.Fatalf("CommissionIntegrationMode() error=%v", err)
	}
	if mode.Enabled {
		t.Fatal("integration mode enabled by default")
	}
	if err := mode.Authorize("Bearer anything", "run-1234"); err != ErrCommissionIntegrationDisabled {
		t.Fatalf("Authorize disabled error=%v", err)
	}
}

func TestCommissionIntegrationModeExplicitFalseStaysDisabled(t *testing.T) {
	mode, err := (&Config{
		AppEnv: "prod", CommissionIntegrationFlag: "false",
		IntegrationControlAddr: "0.0.0.0:1", IntegrationControlToken: "short",
	}).CommissionIntegrationMode()
	if err != nil || mode.Enabled {
		t.Fatalf("explicit false mode=%#v error=%v", mode, err)
	}
}

func TestCommissionIntegrationModeRejectsMalformedCommissionIndexFlag(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("COMMISSION_INTEGRATION_MODE", "true")
	t.Setenv("ENABLE_COMMISSION_INDEX", "not-a-boolean")
	t.Setenv("INTEGRATION_CONTROL_ADDR", "127.0.0.1:18081")
	t.Setenv("INTEGRATION_CONTROL_TOKEN", "0123456789abcdef0123456789abcdef")

	mode, err := Load().CommissionIntegrationMode()
	if err != ErrInvalidCommissionIntegrationConfiguration || mode.Enabled {
		t.Fatalf("malformed index flag mode=%#v error=%v", mode, err)
	}
}

func TestCommissionIntegrationModeAcceptsTestPrivateListeners(t *testing.T) {
	for _, address := range []string{
		"127.0.0.1:18081",
		"10.20.30.40:18081",
		"172.16.4.5:18081",
		"192.168.4.5:18081",
		"[::1]:18081",
		"[fd12:3456::7]:18081",
	} {
		t.Run(address, func(t *testing.T) {
			cfg := &Config{
				AppEnv:                    " TEST ",
				EnableCommissionIndex:     true,
				CommissionIntegrationFlag: "true",
				IntegrationControlAddr:    address,
				IntegrationControlToken:   "  0123456789abcdef0123456789abcdef  ",
			}
			mode, err := cfg.CommissionIntegrationMode()
			if err != nil {
				t.Fatalf("CommissionIntegrationMode() error=%v", err)
			}
			if !mode.Enabled || mode.ControlAddr != address {
				t.Fatalf("mode=%#v", mode)
			}
			if err := mode.Authorize("Bearer 0123456789abcdef0123456789abcdef", "run-1234"); err != nil {
				t.Fatalf("Authorize() error=%v", err)
			}
		})
	}
}

func TestCommissionIntegrationModeRejectsUnsafeConfiguration(t *testing.T) {
	valid := Config{
		AppEnv:                    "test",
		EnableCommissionIndex:     true,
		CommissionIntegrationFlag: "true",
		IntegrationControlAddr:    "127.0.0.1:18081",
		IntegrationControlToken:   "0123456789abcdef0123456789abcdef",
	}
	tests := map[string]func(*Config){
		"malformed flag":   func(c *Config) { c.CommissionIntegrationFlag = "enabled" },
		"production":       func(c *Config) { c.AppEnv = "prod" },
		"index disabled":   func(c *Config) { c.EnableCommissionIndex = false },
		"short token":      func(c *Config) { c.IntegrationControlToken = "0123456789abcdef" },
		"empty address":    func(c *Config) { c.IntegrationControlAddr = "" },
		"localhost":        func(c *Config) { c.IntegrationControlAddr = "localhost:18081" },
		"wildcard IPv4":    func(c *Config) { c.IntegrationControlAddr = "0.0.0.0:18081" },
		"unspecified IPv6": func(c *Config) { c.IntegrationControlAddr = "[::]:18081" },
		"public IPv4":      func(c *Config) { c.IntegrationControlAddr = "8.8.8.8:18081" },
		"public IPv6":      func(c *Config) { c.IntegrationControlAddr = "[2001:4860:4860::8888]:18081" },
		"link local IPv4":  func(c *Config) { c.IntegrationControlAddr = "169.254.1.2:18081" },
		"link local IPv6":  func(c *Config) { c.IntegrationControlAddr = "[fe80::1]:18081" },
		"multicast":        func(c *Config) { c.IntegrationControlAddr = "224.0.0.1:18081" },
		"zero port":        func(c *Config) { c.IntegrationControlAddr = "127.0.0.1:0" },
		"missing port":     func(c *Config) { c.IntegrationControlAddr = "127.0.0.1" },
		"invalid address":  func(c *Config) { c.IntegrationControlAddr = "not-an-address" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			mutate(&cfg)
			mode, err := cfg.CommissionIntegrationMode()
			if err != ErrInvalidCommissionIntegrationConfiguration {
				t.Fatalf("error=%v, want generic invalid configuration", err)
			}
			if mode.Enabled {
				t.Fatalf("unsafe mode enabled: %#v", mode)
			}
		})
	}
}

func TestCommissionIntegrationAuthorizationFailsClosed(t *testing.T) {
	cfg := &Config{
		AppEnv: "test", EnableCommissionIndex: true,
		CommissionIntegrationFlag: "true", IntegrationControlAddr: "127.0.0.1:18081",
		IntegrationControlToken: "0123456789abcdef0123456789abcdef",
	}
	mode, err := cfg.CommissionIntegrationMode()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, authorization, runID string
		want                       error
	}{
		{"missing bearer", "", "run-1234", ErrCommissionIntegrationUnauthorized},
		{"wrong bearer", "Bearer 0123456789abcdef0123456789abcdeg", "run-1234", ErrCommissionIntegrationUnauthorized},
		{"agent token", "Bearer at_0123456789abcdef0123456789abcdef", "run-1234", ErrCommissionIntegrationUnauthorized},
		{"bad run ID", "Bearer 0123456789abcdef0123456789abcdef", "INVALID", ErrInvalidCommissionIntegrationRunID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mode.Authorize(tc.authorization, tc.runID)
			if got != tc.want {
				t.Fatalf("Authorize() error=%v, want %v", got, tc.want)
			}
			if got != nil && ((tc.authorization != "" && got.Error() == tc.authorization) || (tc.runID != "" && got.Error() == tc.runID)) {
				t.Fatal("authorization error echoed request input")
			}
		})
	}
}
