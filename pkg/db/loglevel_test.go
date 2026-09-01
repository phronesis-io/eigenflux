package db

import (
	"testing"

	gormlogger "gorm.io/gorm/logger"
)

func TestLogLevelFromEnv(t *testing.T) {
	cases := map[string]gormlogger.LogLevel{
		"":       gormlogger.Warn,
		"bogus":  gormlogger.Warn,
		"warn":   gormlogger.Warn,
		"silent": gormlogger.Silent,
		"error":  gormlogger.Error,
		"info":   gormlogger.Info,
		"DEBUG":  gormlogger.Info,
		" Info ": gormlogger.Info,
	}
	for in, want := range cases {
		t.Setenv("DB_LOG_LEVEL", in)
		if got := LogLevelFromEnv(); got != want {
			t.Fatalf("DB_LOG_LEVEL=%q: got %v want %v", in, got, want)
		}
	}
}
