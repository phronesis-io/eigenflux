package db_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"eigenflux_server/pkg/db"

	"gorm.io/gorm"
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
		if got := db.LogLevelFromEnv(); got != want {
			t.Fatalf("DB_LOG_LEVEL=%q: got %v want %v", in, got, want)
		}
	}
}

func TestGormLoggerIgnoresRecordNotFoundAndHasNoColours(t *testing.T) {
	var buf bytes.Buffer
	lg := db.NewGormLoggerTo(&buf, gormlogger.Warn)
	sql := func() (string, int64) { return "SELECT * FROM sessions WHERE token_hash = $1", 0 }

	// A missing row is an ordinary answer (invalid bearer token) — never logged,
	// so a stream of bad tokens cannot flood the journal even at Warn.
	lg.Trace(context.Background(), time.Now(), sql, gorm.ErrRecordNotFound)
	if buf.Len() != 0 {
		t.Fatalf("ErrRecordNotFound must not be logged at Warn, got %q", buf.String())
	}

	// A real error is still logged, without ANSI colour escapes (sink is journald).
	lg.Trace(context.Background(), time.Now(), sql, errors.New("connection reset"))
	out := buf.String()
	if !strings.Contains(out, "connection reset") {
		t.Fatalf("real error must be logged at Warn, got %q", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("logger must not emit ANSI colours, got %q", out)
	}

	// A normal fast statement is silent at Warn (this is the whole point).
	buf.Reset()
	lg.Trace(context.Background(), time.Now(), sql, nil)
	if buf.Len() != 0 {
		t.Fatalf("ordinary statement must be silent at Warn, got %q", buf.String())
	}

	// ...and a slow one (>200 ms) is still reported.
	lg.Trace(context.Background(), time.Now().Add(-time.Second), sql, nil)
	if !strings.Contains(buf.String(), "SLOW SQL") {
		t.Fatalf("slow statement must be logged at Warn, got %q", buf.String())
	}
}
