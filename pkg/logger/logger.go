package logger

import (
	"context"
	"log"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/trace"
)

// Init sets up the global slog logger with a JSON handler that writes to
// stdout. If lokiURL is non-empty, a Loki push handler is layered on top.
// Returns a flush function to drain the Loki buffer on shutdown.
func Init(serviceName string, lokiURL string) func() {
	jsonHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}).WithAttrs([]slog.Attr{
		slog.String("service", serviceName),
	})

	var flush func()
	if lokiURL != "" {
		loki := NewLokiHandler(jsonHandler, serviceName, lokiURL)
		slog.SetDefault(slog.New(loki))
		flush = loki.Flush
	} else {
		slog.SetDefault(slog.New(jsonHandler))
		flush = func() {}
	}

	// Bridge stdlib log to slog so any remaining log.Printf calls get
	// captured as structured JSON too.
	slogWriter := &slogBridge{}
	log.SetOutput(slogWriter)
	log.SetFlags(0) // slog handles timestamp/source

	slog.Info("logging initialized")
	return flush
}

// Default returns the configured process-wide logger.
func Default() *slog.Logger {
	return slog.Default()
}

// DebugEnabled reports whether the configured logger will emit debug records.
func DebugEnabled() bool {
	return Default().Enabled(context.Background(), slog.LevelDebug)
}

// Ctx returns a slog.Logger with traceId and spanId fields extracted from the
// OTel span in ctx. If no active span is present, it returns the default
// process logger.
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

// slogBridge adapts slog as an io.Writer for stdlib log.
type slogBridge struct{}

func (b *slogBridge) Write(p []byte) (n int, err error) {
	msg := string(p)
	if len(msg) > 0 && msg[len(msg)-1] == '\n' {
		msg = msg[:len(msg)-1]
	}
	slog.Info(msg)
	return len(p), nil
}
