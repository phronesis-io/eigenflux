package commissionintegration

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"time"

	"eigenflux_server/pkg/config"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

const (
	statusPath                 = "/internal/integration/v1/status"
	diagnosticPath             = "/internal/integration/v1/commissions/:commission_id/diagnostics"
	maxPrivateRequestBodyBytes = 1024
	privateDependencyTimeout   = 5 * time.Second
)

type errorResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func NewServer(mode config.CommissionIntegration, service *Service, listener net.Listener) (*server.Hertz, error) {
	if !mode.Enabled {
		return nil, config.ErrCommissionIntegrationDisabled
	}
	if service == nil || listener == nil || mode.ControlAddr == "" || listener.Addr().String() != mode.ControlAddr {
		return nil, ErrInvalidConfiguration
	}
	h := server.Default(
		server.WithListener(listener),
		server.WithMaxRequestBodySize(maxPrivateRequestBodyBytes),
		server.WithStreamBody(true),
		server.WithHandleMethodNotAllowed(true),
		server.WithReadTimeout(5*time.Second),
		server.WithWriteTimeout(10*time.Second),
		server.WithIdleTimeout(30*time.Second),
	)
	h.Use(authorizeMiddleware(mode))
	h.GET(statusPath, func(ctx context.Context, c *app.RequestContext) {
		if !rejectRequestBody(c) {
			return
		}
		dependencyContext, cancel := context.WithTimeout(ctx, privateDependencyTimeout)
		defer cancel()
		c.JSON(http.StatusOK, service.Status(dependencyContext))
	})
	h.GET(diagnosticPath, func(ctx context.Context, c *app.RequestContext) {
		if !rejectRequestBody(c) {
			return
		}
		commissionID, err := strconv.ParseInt(c.Param("commission_id"), 10, 64)
		if err != nil || commissionID <= 0 {
			writePrivateError(c, http.StatusBadRequest, "invalid request")
			return
		}
		dependencyContext, cancel := context.WithTimeout(ctx, privateDependencyTimeout)
		defer cancel()
		diagnostic, err := service.Diagnostic(dependencyContext, commissionID)
		if err != nil {
			if err == ErrInvalidArgument {
				writePrivateError(c, http.StatusBadRequest, "invalid request")
				return
			}
			writePrivateError(c, http.StatusBadGateway, "dependency unavailable")
			return
		}
		c.JSON(http.StatusOK, diagnostic)
	})
	h.NoMethod(func(_ context.Context, c *app.RequestContext) {
		writePrivateError(c, http.StatusMethodNotAllowed, "method not allowed")
	})
	h.NoRoute(func(_ context.Context, c *app.RequestContext) {
		writePrivateError(c, http.StatusNotFound, "not found")
	})
	return h, nil
}

func authorizeMiddleware(mode config.CommissionIntegration) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		if mode.Authorize(string(c.GetHeader("Authorization")), string(c.GetHeader("X-Integration-Run-ID"))) != nil {
			writePrivateError(c, http.StatusUnauthorized, "unauthorized")
			c.Abort()
			return
		}
		c.Next(ctx)
	}
}

func rejectRequestBody(c *app.RequestContext) bool {
	stream := c.RequestBodyStream()
	if stream == nil {
		return true
	}
	var probe [1]byte
	count, _ := stream.Read(probe[:])
	if count != 0 {
		writePrivateError(c, http.StatusBadRequest, "invalid request")
		c.SetConnectionClose()
		return false
	}
	return true
}

func writePrivateError(c *app.RequestContext, status int, message string) {
	c.JSON(status, errorResponse{Code: status, Msg: message})
}
