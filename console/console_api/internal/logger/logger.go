package logger

import (
	"context"
	"log"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/trace"
)

// Init sets up slog with JSON handler writing to stdout.
func Init(serviceName string) {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}).WithAttrs([]slog.Attr{
		slog.String("service", serviceName),
	})
	slog.SetDefault(slog.New(handler))

	// Bridge stdlib log.
	log.SetOutput(&slogBridge{})
	log.SetFlags(0)

	slog.Info("logging initialized")
}

// FromContext returns a logger with traceId/spanId from OTel context.
func FromContext(ctx context.Context) *slog.Logger {
	span := trace.SpanFromContext(ctx)
	sc := span.SpanContext()
	if sc.HasTraceID() {
		return slog.Default().With(
			"traceId", sc.TraceID().String(),
			"spanId", sc.SpanID().String(),
		)
	}
	return slog.Default()
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
