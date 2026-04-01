package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	PgDSN                string
	RedisAddr            string
	RedisPassword        string
	EtcdAddr             string
	ConsoleApiPort       int
	IDWorkerPrefix       string
	IDSnowflakeEpoch     int64
	IDWorkerLeaseTTL     int
	IDInstanceID         string
	MonitorEnabled       bool
	OtelExporterEndpoint string
	LokiURL              string
	LogLevel             string
}

func Load() *Config {
	loadDotEnv()

	postgresPort := getEnv("POSTGRES_PORT", "5432")
	redisPort := getEnv("REDIS_PORT", "6379")
	etcdPort := getEnv("ETCD_PORT", "2379")

	return &Config{
		PgDSN:                getEnv("PG_DSN", "postgres://eigenflux:eigenflux123@localhost:"+postgresPort+"/eigenflux?sslmode=disable"),
		RedisAddr:            getEnv("REDIS_ADDR", "localhost:"+redisPort),
		RedisPassword:        getEnv("REDIS_PASSWORD", ""),
		EtcdAddr:             getEnv("ETCD_ADDR", "localhost:"+etcdPort),
		ConsoleApiPort:       getEnvInt("CONSOLE_API_PORT", 8090),
		IDWorkerPrefix:       getEnv("ID_WORKER_PREFIX", "/eigenflux/idgen/workers"),
		IDSnowflakeEpoch:     getEnvInt64("ID_SNOWFLAKE_EPOCH_MS", 1704067200000),
		IDWorkerLeaseTTL:     getEnvInt("ID_WORKER_LEASE_TTL", 30),
		IDInstanceID:         getEnv("ID_INSTANCE_ID", ""),
		MonitorEnabled:       getEnvBool("MONITOR_ENABLED", false),
		OtelExporterEndpoint: getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		LokiURL:              getEnv("LOKI_URL", "http://localhost:3122"),
		LogLevel:             getEnv("LOG_LEVEL", "debug"),
	}
}

func (c *Config) ListenAddr() string {
	return fmt.Sprintf(":%d", c.ConsoleApiPort)
}

func (c *Config) EffectiveLokiURL() string {
	if !c.MonitorEnabled {
		return ""
	}
	return c.LokiURL
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
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func EtcdEndpoints(addr string) []string {
	parts := strings.Split(addr, ",")
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
