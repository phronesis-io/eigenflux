package logger

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// Init sets up slog with JSON handler writing to stdout + log file.
func Init(logDir string, serviceName string) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		log.Fatalf("failed to create log directory %s: %v", logDir, err)
	}

	filename := time.Now().Format("20060102_150405") + ".log"
	path := filepath.Join(logDir, filename)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Fatalf("failed to open log file %s: %v", path, err)
	}

	w := io.MultiWriter(os.Stdout, f)
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}).WithAttrs([]slog.Attr{
		slog.String("service", serviceName),
	})
	slog.SetDefault(slog.New(handler))

	// Bridge stdlib log.
	log.SetOutput(&slogBridge{})
	log.SetFlags(0)

	slog.Info("logging initialized", "dir", logDir, "file", path)
	fmt.Println()
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
