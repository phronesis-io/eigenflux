package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Init sets up the global OTel TracerProvider with an OTLP gRPC exporter.
// Call the returned shutdown function on service exit to flush pending spans.
// When enabled is false, sets up propagation only (no exporter) so trace
// context still flows through the system without exporting spans.
func Init(serviceName string, endpoint string, enabled bool) (shutdown func(context.Context), err error) {
	// Always set up propagation so trace headers are forwarded even when
	// tracing is disabled.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if !enabled {
		slog.Info("tracing disabled", "service", serviceName)
		return func(context.Context) {}, nil
	}

	ctx := context.Background()

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceNameKey.String(serviceName)),
	)
	if err != nil {
		return nil, err
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	slog.Info("tracing enabled", "service", serviceName, "endpoint", endpoint)
	return func(ctx context.Context) {
		_ = tp.Shutdown(ctx)
	}, nil
}
