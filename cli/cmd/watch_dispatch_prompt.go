package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/dispatch"
)

// Preserve the failing preparation stage without persisting API bodies, paths,
// credentials or message content. Unwrap retains identity/auth classifications.
type dispatchPromptError struct {
	code  string
	cause error
}

func (e *dispatchPromptError) Error() string { return e.code }
func (e *dispatchPromptError) Unwrap() error { return e.cause }

func dispatchPromptFailureCode(err error) string {
	if errors.Is(err, context.Canceled) {
		return "prompt_cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "prompt_timeout"
	}
	var failure *dispatchPromptError
	if errors.As(err, &failure) {
		var api *client.APIError
		if errors.As(err, &api) && api.StatusCode >= 400 && api.StatusCode <= 599 {
			return fmt.Sprintf("%s_http_%d", failure.code, api.StatusCode)
		}
		var network net.Error
		if errors.As(err, &network) {
			if network.Timeout() {
				return failure.code + "_timeout"
			}
			return failure.code + "_network"
		}
		return failure.code
	}
	return "prompt_unavailable"
}

func retryableDispatchPrompt(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, dispatch.ErrNeedsUser) {
		return false
	}
	var failure *dispatchPromptError
	if !errors.As(err, &failure) || (failure.code != "prompt_control_context_unavailable" && failure.code != "prompt_history_unavailable") {
		return false
	}
	var api *client.APIError
	if errors.As(err, &api) {
		return api.StatusCode == http.StatusBadGateway || api.StatusCode == http.StatusServiceUnavailable || api.StatusCode == http.StatusGatewayTimeout
	}
	var network net.Error
	return errors.As(err, &network)
}

// Only retry read-only PM preparation. Never replay an Agent invocation, a
// destructive PM fetch, a send, or an optional task subprocess.
func (w *accountWatch) prepareDispatchPrompt(ctx context.Context, job dispatch.Job) (string, error) {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		prompt, err := w.dispatchPrompt(ctx, job)
		if err == nil || job.Kind != "pm_push" || attempt >= 2 || !retryableDispatchPrompt(err) {
			return prompt, err
		}
		if err := w.dispatchIdentityOK(); err != nil {
			return "", err
		}
		if !watchPause(ctx, time.Duration(attempt+1)*100*time.Millisecond) {
			return "", ctx.Err()
		}
	}
}
