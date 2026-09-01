package db_test

import (
	"testing"

	"console.eigenflux.ai/internal/db"

	gormlogger "gorm.io/gorm/logger"
)

func TestGormLogLevel(t *testing.T) {
	cases := map[string]gormlogger.LogLevel{
		"":       gormlogger.Info, // historical console default
		"info":   gormlogger.Info,
		"DEBUG":  gormlogger.Info,
		"warn":   gormlogger.Warn,
		"error":  gormlogger.Error,
		"silent": gormlogger.Silent,
		"wrn":    gormlogger.Warn, // typo must never re-enable per-statement logging
		" bogus": gormlogger.Warn,
	}
	for in, want := range cases {
		t.Setenv("DB_LOG_LEVEL", in)
		if got := db.GormLogLevel(); got != want {
			t.Fatalf("DB_LOG_LEVEL=%q: got %v want %v", in, got, want)
		}
	}
}
