package cmd

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
)

// refreshPinnedV2Credentials preserves the watch account through lock waits and
// token rotation. Cancellation releases the refresh lock before returning.
func refreshPinnedV2Credentials(ctx context.Context, serverName, endpoint string, force bool, agentID, principalID string) (*auth.V2Credentials, error) {
	var credentials *auth.V2Credentials
	guard := func(c *auth.V2Credentials) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if c == nil || c.AgentID != agentID || c.PrincipalID != principalID {
			return errWatchIdentity
		}
		return nil
	}
	err := auth.WithV2CredentialsLockContext(ctx, serverName, 35*time.Second, func() error {
		current, err := auth.LoadV2Credentials(serverName)
		if err != nil {
			return err
		}
		if err := guard(current); err != nil {
			return err
		}
		if _, _, err := auth.LoadIdentity(serverName); err != nil {
			return err
		}
		credentials, err = ensureV2CredentialsUnlockedWithGuard(serverName, endpoint, force, guard, func(c *client.Client) {
			c.HTTPClient.Transport = pinnedRefreshTransport{parent: ctx, base: http.DefaultTransport}
		})
		return err
	})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return credentials, err
}

// Preserve net/http's request deadline while joining the watch cancellation.
// Keep the cancellation attached until the response body has been closed.
type pinnedRefreshTransport struct {
	parent context.Context
	base   http.RoundTripper
}

func (t pinnedRefreshTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(t.parent, cancel)
	cleanup := func() { stop(); cancel() }
	if err := t.parent.Err(); err != nil {
		cleanup()
		return nil, err
	}
	response, err := t.base.RoundTrip(r.WithContext(ctx))
	if err != nil {
		cleanup()
		return response, err
	}
	response.Body = &pinnedRefreshBody{ReadCloser: response.Body, cleanup: cleanup}
	return response, nil
}

type pinnedRefreshBody struct {
	io.ReadCloser
	cleanup func()
	once    sync.Once
}

func (b *pinnedRefreshBody) Close() error {
	defer b.once.Do(b.cleanup)
	return b.ReadCloser.Close()
}
