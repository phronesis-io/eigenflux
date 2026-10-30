package cmd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"cli.eigenflux.ai/internal/client"
)

// readOrderWithEOFRecovery is limited to the side-effect-free order detail GET.
// It must not be used for transfers, mutations, or Agent execution.
func readOrderWithEOFRecovery(ctx context.Context, c *client.Client, path string) (*client.APIResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	httpClient := *c.HTTPClient
	transport := httpClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	localClient := *c
	localClient.HTTPClient = &httpClient
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var sawHTTPError atomic.Bool
		requestContext := ctx
		cancel := func() {}
		if httpClient.Timeout > 0 {
			requestContext, cancel = context.WithTimeout(ctx, httpClient.Timeout)
		}
		httpClient.Transport = orderReadTransport{ctx: requestContext, transport: transport, sawHTTPError: &sawHTTPError}
		resp, err := localClient.Get(path, nil)
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil || sawHTTPError.Load() || attempt == 2 || (!errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF)) {
			return resp, err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

type orderReadTransport struct {
	ctx          context.Context
	transport    http.RoundTripper
	sawHTTPError *atomic.Bool
}

func (t orderReadTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.transport.RoundTrip(req.Clone(t.ctx))
	if resp != nil && resp.StatusCode >= http.StatusBadRequest {
		t.sawHTTPError.Store(true)
	}
	return resp, err
}
