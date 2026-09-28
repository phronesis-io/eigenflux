package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"cli.eigenflux.ai/internal/auth"
)

const commissionPollPages = 32

type commissionNotificationPage struct {
	Notifications []json.RawMessage `json:"notifications"`
	HasMore       bool              `json:"has_more"`
	NextCursor    string            `json:"next_cursor"`
}

func decodeCommissionNotificationPage(data json.RawMessage) (commissionNotificationPage, error) {
	var page commissionNotificationPage
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil ||
		len(fields["notifications"]) == 0 || bytes.Equal(bytes.TrimSpace(fields["notifications"]), []byte("null")) ||
		json.Unmarshal(data, &page) != nil || len(page.Notifications) > 100 {
		return page, errors.New("invalid_commission_notification_page")
	}
	return page, nil
}

func (w *accountWatch) receivesCommission() bool {
	return w.binding != nil && w.journal != nil && w.binding.Handles("commission_order")
}

func (w *accountWatch) deliverSocketEvent(ctx context.Context, kind string, data json.RawMessage) error {
	if kind == "notification_push" && w.receivesCommission() {
		return w.deliverCommissionNotifications(ctx, data, "socket")
	}
	return w.deliverPM(ctx, kind, data, "socket")
}

// Notification-only bindings use HTTP: the current server WebSocket also marks
// PMs read. A binding owning both sources can reuse its existing PM connection.
func (w *accountWatch) commissionFallbackLoop(ctx context.Context) error {
	if !w.receivesCommission() {
		return nil
	}
	for ctx.Err() == nil {
		if err := w.pollCommissionNotifications(ctx); err != nil {
			if fatal := w.diagnostic("commission_poll", err); fatal != nil {
				return fatal
			}
		}
		if !watchPause(ctx, time.Minute) {
			break
		}
	}
	return ctx.Err()
}

func (w *accountWatch) pollCommissionNotifications(ctx context.Context) error {
	if !w.receivesCommission() {
		return nil
	}
	cursor := ""
	seen := map[string]bool{"": true}
	for pageIndex := 0; pageIndex < commissionPollPages; pageIndex++ {
		if err := w.dispatchIdentityOK(); err != nil {
			return err
		}
		api, err := w.api(ctx)
		if err != nil {
			return err
		}
		params := map[string]string{"limit": "50"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		response, err := api.Get("/notifications/pending", params)
		if err != nil {
			return err
		}
		if response.Code != 0 {
			return errors.New("commission_poll_failed")
		}
		page, err := decodeCommissionNotificationPage(response.Data)
		if err != nil {
			return err
		}
		if err := w.deliverCommissionNotifications(ctx, response.Data, "http_poll"); err != nil {
			return err
		}
		if !page.HasMore {
			return nil
		}
		if seen[page.NextCursor] {
			return errors.New("commission_notification_cursor_did_not_advance")
		}
		cursor = page.NextCursor
		seen[cursor] = true
	}
	return nil
}

func (w *accountWatch) deliverCommissionNotifications(ctx context.Context, data json.RawMessage, transport string) error {
	if !w.receivesCommission() {
		return nil
	}
	page, err := decodeCommissionNotificationPage(data)
	if err != nil {
		return err
	}
	api, err := w.api(ctx)
	if err != nil {
		return err
	}
	return auth.WithV2CredentialsLockContext(ctx, w.server.Name, 35*time.Second, func() error {
		if err := w.dispatchIdentityOK(); err != nil {
			return err
		}
		ack := make([]map[string]string, 0, len(page.Notifications))
		for _, raw := range page.Notifications {
			var notification struct {
				Source string          `json:"source_type"`
				ID     json.RawMessage `json:"notification_id"`
			}
			if json.Unmarshal(raw, &notification) != nil {
				return errors.New("invalid_commission_notification")
			}
			if notification.Source != "commission_order" {
				continue
			}
			if err := w.journal.AddCommissionNotification(raw); err != nil {
				return errors.Join(errWatchDispatchJournal, err)
			}
			var id notificationID
			if json.Unmarshal(notification.ID, &id) != nil {
				return errors.New("invalid_commission_notification_id")
			}
			ack = append(ack, map[string]string{
				"notification_id": strconv.FormatInt(int64(id), 10),
				"source_type":     "commission_order",
			})
		}
		// Every notification is durable before any ACK. Refresh outside the
		// credential lock; an uncertain ACK is safe to repeat after deduplication.
		api.OnUnauthorized = nil
		credentials, err := auth.LoadV2Credentials(w.server.Name)
		if err != nil {
			return err
		}
		api.Token = credentials.AccessToken
		for first := 0; first < len(ack); first += 50 {
			last := min(first+50, len(ack))
			response, err := api.Post("/notifications/ack", map[string]interface{}{"notifications": ack[first:last]})
			if err != nil {
				return err
			}
			if response.Code != 0 {
				return errors.New("commission_notification_ack_failed")
			}
		}
		return w.emit("notification_push", map[string]interface{}{
			"notifications": page.Notifications,
			"has_more":      page.HasMore,
			"next_cursor":   page.NextCursor,
			"transport":     transport,
			"received_at":   time.Now().UTC().Format(time.RFC3339Nano),
		})
	})
}
