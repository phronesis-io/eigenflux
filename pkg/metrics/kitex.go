package metrics

import (
	"context"
	"time"

	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/server"
)

func KitexServerMW() server.Option {
	return server.WithMiddleware(func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, req, resp interface{}) error {
			ri := rpcinfo.GetRPCInfo(ctx)
			if ri == nil || ri.To() == nil {
				return next(ctx, req, resp)
			}
			service := ri.To().ServiceName()
			method := ri.To().Method()

			start := time.Now()
			err := next(ctx, req, resp)
			duration := time.Since(start).Seconds()

			status := "ok"
			if err != nil {
				status = "error"
			}

			RPCRequestDuration.WithLabelValues(service, method, status).Observe(duration)
			RPCRequestsTotal.WithLabelValues(service, method, status).Inc()
			return err
		}
	})
}
