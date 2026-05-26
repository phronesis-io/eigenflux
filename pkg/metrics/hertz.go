package metrics

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/cloudwego/hertz/pkg/app"
)

func HertzMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		HTTPRequestsInFlight.Inc()
		start := time.Now()

		c.Next(ctx)

		HTTPRequestsInFlight.Dec()
		duration := time.Since(start).Seconds()
		method := string(c.Method())
		path := normalizePath(string(c.Request.URI().Path()))
		status := strconv.Itoa(c.Response.StatusCode())

		HTTPRequestDuration.WithLabelValues(method, path, status).Observe(duration)
		HTTPRequestsTotal.WithLabelValues(method, path, status).Inc()
	}
}

func normalizePath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == "" {
			continue
		}
		if isNumeric(part) {
			parts[i] = ":id"
		}
	}
	return strings.Join(parts, "/")
}

func isNumeric(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return len(s) > 0
}
