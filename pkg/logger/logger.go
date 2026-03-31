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

// Init sets up the global slog logger with a JSON handler that writes to
// both stdout and a timestamped log file. If lokiURL is non-empty, a Loki
// push handler is added. serviceName is embedded in every log record.
// Returns a flush function to drain the Loki buffer on shutdown.
func Init(logDir string, serviceName string, lokiURL string) func() {
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

	jsonHandler := slog.NewJSONHandler(w, &slog.HandlerOptions{
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

	slog.Info("logging initialized", "dir", logDir, "file", path)
	fmt.Println() // blank line for readability after init
	return flush
}

// FromContext returns a slog.Logger with traceId and spanId fields
// extracted from the OTel span in ctx. If no active span, returns the
// default logger without trace fields.
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
