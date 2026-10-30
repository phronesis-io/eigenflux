package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"cli.eigenflux.ai/internal/auth"
)

func (w *accountWatch) recoverUnrecordedWorkBuddyOrders(ctx context.Context) error {
	api, err := w.commissionAPI(ctx)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, state := range []string{"awaiting_seller", "in_progress"} {
		cursor := ""
		seen := map[string]bool{"": true}
		complete := false
		for page := 0; page < 32; page++ {
			if err := w.commissionIdentityOK(); err != nil {
				return err
			}
			params := map[string]string{"role": "seller", "state": state, "limit": "100"}
			if cursor != "" {
				params["cursor"] = cursor
			}
			response, err := api.Get("/orders", params)
			if err != nil {
				return fmt.Errorf("WorkBuddy order migration lookup: %w", err)
			}
			var data struct {
				Orders json.RawMessage `json:"orders"`
				Next   *string         `json:"next_cursor"`
			}
			if response.Code != 0 || json.Unmarshal(response.Data, &data) != nil || len(data.Orders) == 0 || string(data.Orders) == "null" || data.Next == nil {
				return errors.New("invalid WorkBuddy order migration page")
			}
			var orders []struct {
				ID      notificationID `json:"order_id"`
				Seller  notificationID `json:"seller_agent_id"`
				State   string         `json:"state"`
				Version int64          `json:"version"`
			}
			if json.Unmarshal(data.Orders, &orders) != nil || len(orders) > 100 {
				return errors.New("invalid WorkBuddy order migration orders")
			}
			for _, listed := range orders {
				id := strconv.FormatInt(int64(listed.ID), 10)
				if listed.ID <= 0 || listed.Version <= 0 || strconv.FormatInt(int64(listed.Seller), 10) != w.binding.AgentID || listed.State != state {
					return errors.New("WorkBuddy order migration identity or state mismatch")
				}
				if known[id] || w.journal.HasRecordedCommissionOrder(id) {
					continue
				}
				order, err := w.readCommissionOrder(ctx, id)
				if err != nil {
					return err
				}
				if order.State != "awaiting_seller" && order.State != "in_progress" {
					continue
				}
				err = auth.WithV2CredentialsLockContext(ctx, w.server.Name, commissionIntakeTimeout, func() error {
					if err := w.commissionIdentityOK(); err != nil {
						return err
					}
					_, err := w.journal.RecoverUnrecordedCommission(id, order.Version)
					return err
				})
				if err != nil {
					return err
				}
				known[id] = true
			}
			cursor = *data.Next
			if cursor == "" {
				complete = true
				break
			}
			if seen[cursor] {
				return errors.New("WorkBuddy order migration cursor did not advance")
			}
			seen[cursor] = true
		}
		if !complete {
			return errors.New("WorkBuddy order migration page limit reached; migration remains incomplete")
		}
	}
	return w.commissionIdentityOK()
}
