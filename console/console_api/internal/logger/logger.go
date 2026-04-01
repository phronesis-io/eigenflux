package logger

import (
	"context"
	"log"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// Init sets up slog with JSON handler writing to stdout, optionally pushing
// the same records to Loki when lokiURL is configured. It returns a flush
// function that should be called on shutdown.
func Init(serviceName string, lokiURL string, level string) func() {
	logLevelName := normalizeLevel(level)
	logLevel := parseLevel(logLevelName)
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}).WithAttrs([]slog.Attr{
		slog.String("service", serviceName),
	})

	var flush func()
	if lokiURL != "" {
		loki := NewLokiHandler(handler, serviceName, lokiURL)
		slog.SetDefault(slog.New(loki))
		flush = loki.Flush
	} else {
		slog.SetDefault(slog.New(handler))
		flush = func() {}
	}

	// Bridge stdlib log.
	log.SetOutput(&slogBridge{})
	log.SetFlags(0)

	Default().Info("logging initialized", "level", logLevelName)
	return flush
}

// Default returns the configured process-wide logger.
func Default() *slog.Logger {
	return slog.Default()
}

// Ctx returns a logger with traceId/spanId from OTel context.
func Ctx(ctx context.Context) *slog.Logger {
	span := trace.SpanFromContext(ctx)
	sc := span.SpanContext()
	if sc.HasTraceID() {
		return Default().With(
			"traceId", sc.TraceID().String(),
			"spanId", sc.SpanID().String(),
		)
	}
	return Default()
}

type slogBridge struct{}

func (b *slogBridge) Write(p []byte) (n int, err error) {
	msg := string(p)
	if len(msg) > 0 && msg[len(msg)-1] == '\n' {
		msg = msg[:len(msg)-1]
	}
	slog.Info(msg)
	return len(p), nil
}

func parseLevel(raw string) slog.Level {
	switch normalizeLevel(raw) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelDebug
	}
}

func normalizeLevel(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "debug":
		return "debug"
	case "info":
		return "info"
	case "warn", "warning":
		return "warn"
	case "error":
		return "error"
	default:
		return "debug"
	}
}
