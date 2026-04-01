package middleware

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"go.opentelemetry.io/otel/trace"
)

// TraceIDMiddleware writes the current OTel traceId to the X-Trace-Id
// response header. Must be registered after the OTel tracing middleware
// so the span already exists in the context.
func TraceIDMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.Next(ctx)
		sc := trace.SpanFromContext(ctx).SpanContext()
		if sc.HasTraceID() {
			c.Header("X-Trace-Id", sc.TraceID().String())
		}
	}
}
