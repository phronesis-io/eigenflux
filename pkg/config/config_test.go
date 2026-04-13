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
