package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"cli.eigenflux.ai/internal/auth"
)

// Account switching uses the credential lock to clear identity-scoped caches.
func (w *accountWatch) deliverPM(ctx context.Context, kind string, data json.RawMessage, transport string) error {
	return auth.WithV2CredentialsLockContext(ctx, w.server.Name, 35*time.Second, func() error {
		credentials, err := auth.LoadV2Credentials(w.server.Name)
		if err != nil {
			return err
		}
		if !w.identityOK(credentials) {
			return errWatchIdentity
		}
		cacheMessagesForServer(data, w.server.Name)
		if kind == "pm_push" {
			var logged map[string]json.RawMessage
			if json.Unmarshal(data, &logged) != nil || logged == nil {
				return errors.New("invalid_pm_response")
			}
			logged["transport"], _ = json.Marshal(transport)
			logged["received_at"], _ = json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))
			return w.emit(kind, logged)
		}
		return w.emit(kind, data)
	})
}

func (w *accountWatch) pmFallbackLoop(ctx context.Context) error {
	for watchPause(ctx, time.Minute) {
		if err := w.pollPM(ctx); err != nil {
			if fatal := w.diagnostic("pm_poll", err); fatal != nil {
				return fatal
			}
		}
	}
	return ctx.Err()
}

func (w *accountWatch) pollPM(ctx context.Context) error {
	cursor := ""
	for page := 0; page < 32; page++ {
		next, more, err := w.pollPMPage(ctx, cursor)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
		cursor = next
	}
	return nil
}

func (w *accountWatch) pollPMPage(ctx context.Context, cursor string) (string, bool, error) {
	if !w.handoff.begin() {
		return "", false, nil
	}
	defer w.handoff.end()
	c, err := w.api(ctx)
	if err != nil {
		return "", false, err
	}
	// Fetch marks rows read before returning. Drain through caching and stdout
	// delivery before allowing an update to cancel the old account watch.
	params := map[string]string{"limit": "1"}
	if cursor != "" {
		params["cursor"] = cursor
	}
	resp, err := c.Get("/pm/fetch", params)
	if err != nil {
		return "", false, err
	}
	if resp.Code != 0 {
		return "", false, errors.New("pm_poll_failed")
	}
	var batch struct {
		Messages   []json.RawMessage `json:"messages"`
		NextCursor string            `json:"next_cursor"`
	}
	if json.Unmarshal(resp.Data, &batch) != nil {
		return "", false, errors.New("invalid_pm_response")
	}
	if err := w.deliverPM(ctx, "pm_push", resp.Data, "http_poll"); err != nil {
		return "", false, err
	}
	return batch.NextCursor, len(batch.Messages) > 0 && batch.NextCursor != "" && batch.NextCursor != cursor, nil
}
