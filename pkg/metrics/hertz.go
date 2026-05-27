package metrics

import (
	"context"
	"strconv"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

func HertzMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		HTTPRequestsInFlight.Inc()
		defer HTTPRequestsInFlight.Dec()
		start := time.Now()

		c.Next(ctx)

		duration := time.Since(start).Seconds()
		method := string(c.Method())
		path := c.FullPath()
		if path == "" {
			path = "not_found"
		}
		status := strconv.Itoa(c.Response.StatusCode())

		HTTPRequestDuration.WithLabelValues(method, path, status).Observe(duration)
		HTTPRequestsTotal.WithLabelValues(method, path, status).Inc()
	}
}
