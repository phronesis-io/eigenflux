package reqinfo

import (
	"context"
	"time"
)

type requestStartKey struct{}

// WithRequestStart records the first server entry time. Nested middleware and
// handlers preserve it, including time spent waiting for authentication.
func WithRequestStart(ctx context.Context) context.Context {
	if RequestStartedAt(ctx) > 0 {
		return ctx
	}
	return context.WithValue(ctx, requestStartKey{}, time.Now().UnixMilli())
}

// RequestStartedAt returns server request entry time in milliseconds, or zero
// outside an HTTP request. It is local context state, never a client header.
func RequestStartedAt(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	value, _ := ctx.Value(requestStartKey{}).(int64)
	return value
}
