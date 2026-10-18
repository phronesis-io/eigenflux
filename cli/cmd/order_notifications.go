package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/client"
)

type orderNotificationPage struct {
	Notifications []orderNotification `json:"notifications"`
	HasMore       bool                `json:"has_more"`
	NextCursor    string              `json:"next_cursor"`
}

type orderNotification struct {
	NotificationID string                   `json:"notification_id"`
	SourceType     string                   `json:"source_type"`
	Type           string                   `json:"type"`
	CreatedAt      int64                    `json:"created_at"`
	Payload        orderNotificationPayload `json:"payload"`
}

type orderNotificationPayload struct {
	OrderID          notificationID `json:"order_id"`
	OrderVersion     int64          `json:"order_version"`
	RecipientRole    string         `json:"recipient_role"`
	State            string         `json:"state"`
	FromState        string         `json:"from_state"`
	ToState          string         `json:"to_state"`
	CommandKind      string         `json:"command_kind"`
	ActorKind        string         `json:"actor_kind"`
	EventKind        string         `json:"event_kind"`
	SnapshotID       notificationID `json:"snapshot_id"`
	OccurredAt       int64          `json:"occurred_at"`
	PaymentDueAt     int64          `json:"payment_due_at"`
	SellerConfirmDue int64          `json:"seller_confirm_due_at"`
	DeliveryDueAt    int64          `json:"delivery_due_at"`
	BuyerConfirmDue  int64          `json:"buyer_confirm_due_at"`
}

type notificationID int64

func (id *notificationID) UnmarshalJSON(raw []byte) error {
	text := strings.TrimSpace(string(raw))
	if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		text = value
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid notification ID: %w", err)
	}
	*id = notificationID(value)
	return nil
}

func (id notificationID) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatInt(int64(id), 10))
}

func drainOrderNotifications(api *client.Client, format, language string, writer io.Writer) error {
	if api == nil {
		return nil
	}
	cursor := ""
	for {
		params := map[string]string{"limit": "50"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		response, err := api.Get("/notifications/pending", params)
		if err != nil {
			return fmt.Errorf("list pending notifications: %w", err)
		}
		if response.Code != 0 {
			return fmt.Errorf("list pending notifications: %s", response.Msg)
		}
		var page orderNotificationPage
		if err := json.Unmarshal(response.Data, &page); err != nil {
			return fmt.Errorf("parse pending notifications: %w", err)
		}
		ackItems := make([]map[string]string, 0, len(page.Notifications))
		for _, notification := range page.Notifications {
			if notification.SourceType != "commission_order" {
				continue
			}
			if err := validateOrderNotification(notification); err != nil {
				return err
			}
			if err := renderOrderNotification(writer, format, language, notification); err != nil {
				return fmt.Errorf("write Order notification: %w", err)
			}
			ackItems = append(ackItems, map[string]string{
				"notification_id": notification.NotificationID,
				"source_type":     notification.SourceType,
			})
		}
		if len(ackItems) > 0 {
			ackResponse, err := api.Post("/notifications/ack", map[string]interface{}{"notifications": ackItems})
			if err != nil {
				return fmt.Errorf("acknowledge Order notifications: %w", err)
			}
			if ackResponse.Code != 0 {
				return fmt.Errorf("acknowledge Order notifications: %s", ackResponse.Msg)
			}
		}
		if !page.HasMore {
			return nil
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			return fmt.Errorf("pending notification cursor did not advance")
		}
		cursor = page.NextCursor
	}
}

func validateOrderNotification(notification orderNotification) error {
	notificationID, err := strconv.ParseInt(notification.NotificationID, 10, 64)
	if err != nil || notificationID <= 0 {
		return fmt.Errorf("invalid Order notification ID")
	}
	payload := notification.Payload
	if payload.OrderID <= 0 || payload.OrderVersion <= 0 || payload.SnapshotID <= 0 || payload.OccurredAt <= 0 ||
		(payload.RecipientRole != "buyer" && payload.RecipientRole != "seller") || payload.ToState == "" {
		return fmt.Errorf("invalid Order notification payload %s", notification.NotificationID)
	}
	return nil
}

func renderOrderNotification(writer io.Writer, format, language string, notification orderNotification) error {
	if format == "json" {
		return json.NewEncoder(writer).Encode(map[string]interface{}{
			"type": "commission_order_notification", "notification": notification,
		})
	}
	role, summary := localizedOrderNotification(language, notification.Payload.RecipientRole, notification.Payload.ToState)
	timestamp := time.UnixMilli(notification.Payload.OccurredAt).Format("2006-01-02 15:04:05")
	_, err := fmt.Fprintf(writer, "[%s] %s #%d v%d · %s · %s\n", timestamp,
		localizedOrderLabel(language), int64(notification.Payload.OrderID), notification.Payload.OrderVersion, role, summary)
	return err
}

func localizedOrderLabel(language string) string {
	if language == "zh" {
		return "订单"
	}
	return "Order"
}

func localizedOrderNotification(language, role, state string) (string, string) {
	if language == "zh" {
		roleName := map[string]string{"buyer": "买方", "seller": "卖方"}[role]
		if roleName == "" {
			roleName = "参与方"
		}
		summary := map[string]string{
			"awaiting_payment": "等待买方付款", "pending_payment": "等待买方付款",
			"awaiting_materials": "历史材料状态（已废弃，系统自动推进）", "preparing_materials": "历史材料状态（已废弃，系统自动推进）",
			"awaiting_seller": "系统自动接单中", "in_progress": "卖方履约中",
			"validating": "平台验收中", "delivered": "交付待买方验收",
			"awaiting_buyer_confirmation": "交付待买方确认", "completed": "订单已完成",
			"cancelled": "订单已取消", "rejected": "订单已拒绝", "expired": "订单已过期",
			"refunding": "退款处理中", "refund_pending": "退款处理中", "refunded": "订单已退款",
		}[state]
		if summary == "" {
			summary = "订单已更新"
		}
		return roleName, summary
	}
	roleName := map[string]string{"buyer": "Buyer", "seller": "Seller"}[role]
	if roleName == "" {
		roleName = "Participant"
	}
	summary := map[string]string{
		"awaiting_payment": "Waiting for buyer payment", "pending_payment": "Waiting for buyer payment",
		"awaiting_materials": "Retired material state; automatic transition pending", "preparing_materials": "Retired material state; automatic transition pending",
		"awaiting_seller": "Automatic acceptance in progress", "in_progress": "Fulfillment in progress",
		"validating": "Platform validation in progress", "delivered": "Delivery awaiting buyer review",
		"awaiting_buyer_confirmation": "Delivery awaiting buyer confirmation", "completed": "Order completed",
		"cancelled": "Order cancelled", "rejected": "Order rejected", "expired": "Order expired",
		"refunding": "Refund in progress", "refund_pending": "Refund in progress", "refunded": "Order refunded",
	}[state]
	if summary == "" {
		summary = "Order updated"
	}
	return roleName, summary
}
