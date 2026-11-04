package dal

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"eigenflux_server/pkg/notificationpayload"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const PendingRetention = 90 * 24 * time.Hour

const maxDeliveryErrorRunes = 2000

type InboxNotification struct {
	NotificationID    int64  `gorm:"column:notification_id;primaryKey"`
	AgentID           int64  `gorm:"column:agent_id"`
	SourceType        string `gorm:"column:source_type"`
	SourceID          int64  `gorm:"column:source_id"`
	DedupeKey         string `gorm:"column:dedupe_key"`
	EventKind         string `gorm:"column:event_kind"`
	OrderID           int64  `gorm:"column:order_id"`
	OrderVersion      int64  `gorm:"column:order_version"`
	PayloadJSON       string `gorm:"column:payload_json"`
	OccurredAt        int64  `gorm:"column:occurred_at"`
	ReceivedAt        int64  `gorm:"column:received_at"`
	ExpiresAt         int64  `gorm:"column:expires_at"`
	AcknowledgedAt    *int64 `gorm:"column:acknowledged_at"`
	LastDeliveryError string `gorm:"column:last_delivery_error"`
}

func (InboxNotification) TableName() string { return "notification_inbox" }

func InsertInboxNotification(ctx context.Context, db *gorm.DB, value *InboxNotification) (bool, error) {
	if db == nil || value == nil || value.NotificationID <= 0 || value.AgentID <= 0 ||
		!validInboxSource(value.SourceType) || value.SourceID <= 0 || value.DedupeKey == "" ||
		value.EventKind == "" || !validInboxOrderFields(value) || value.PayloadJSON == "" ||
		value.OccurredAt <= 0 || value.ReceivedAt <= 0 || value.ExpiresAt <= value.ReceivedAt {
		return false, errors.New("invalid inbox notification")
	}
	result := db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(value)
	if result.Error != nil || result.RowsAffected == 1 {
		return result.RowsAffected == 1, result.Error
	}
	var existing InboxNotification
	if err := db.WithContext(ctx).
		Where("notification_id = ? OR dedupe_key = ?", value.NotificationID, value.DedupeKey).
		Take(&existing).Error; err != nil {
		return false, err
	}
	if existing.NotificationID != value.NotificationID || existing.AgentID != value.AgentID ||
		existing.SourceType != value.SourceType || existing.SourceID != value.SourceID ||
		existing.DedupeKey != value.DedupeKey || existing.EventKind != value.EventKind ||
		existing.OrderID != value.OrderID || existing.OrderVersion != value.OrderVersion ||
		!sameJSON(existing.PayloadJSON, value.PayloadJSON) || existing.OccurredAt != value.OccurredAt {
		return false, errors.New("inbox notification conflict does not match existing fact")
	}
	return false, nil
}

func validInboxSource(source string) bool {
	return source == SourceTypeCommissionOrder || source == notificationpayload.SourceCommissionModeration
}

func validInboxOrderFields(value *InboxNotification) bool {
	if value.SourceType == notificationpayload.SourceCommissionModeration {
		return value.OrderID == 0 && value.OrderVersion == 0
	}
	return value.OrderID > 0 && value.OrderVersion > 0
}

func sameJSON(left, right string) bool {
	var leftValue, rightValue interface{}
	return json.Unmarshal([]byte(left), &leftValue) == nil && json.Unmarshal([]byte(right), &rightValue) == nil &&
		reflect.DeepEqual(leftValue, rightValue)
}

func SetInboxDeliveryError(ctx context.Context, db *gorm.DB, notificationID, agentID int64, deliveryError string) error {
	if db == nil || notificationID <= 0 || agentID <= 0 {
		return errors.New("invalid inbox delivery error update")
	}
	deliveryError = strings.TrimSpace(deliveryError)
	if runes := []rune(deliveryError); len(runes) > maxDeliveryErrorRunes {
		deliveryError = string(runes[:maxDeliveryErrorRunes])
	}
	return db.WithContext(ctx).Model(&InboxNotification{}).
		Where("notification_id = ? AND agent_id = ? AND source_type IN ?", notificationID, agentID, []string{SourceTypeCommissionOrder, notificationpayload.SourceCommissionModeration}).
		Update("last_delivery_error", deliveryError).Error
}

func ListInboxNotifications(ctx context.Context, db *gorm.DB, agentID, now int64) ([]InboxNotification, error) {
	if db == nil || agentID <= 0 || now <= 0 {
		return nil, errors.New("invalid inbox list request")
	}
	var rows []InboxNotification
	err := db.WithContext(ctx).
		Where("agent_id = ? AND source_type IN ? AND acknowledged_at IS NULL AND expires_at > ?", agentID, []string{SourceTypeCommissionOrder, notificationpayload.SourceCommissionModeration}, now).
		Order("occurred_at ASC, order_version ASC, notification_id ASC").Find(&rows).Error
	return rows, err
}

func AckInboxNotificationsBySource(ctx context.Context, db *gorm.DB, agentID int64, source string, notificationIDs []int64, acknowledgedAt int64) error {
	if db == nil || agentID <= 0 || !validInboxSource(source) || len(notificationIDs) == 0 || acknowledgedAt <= 0 {
		return errors.New("invalid inbox acknowledgement")
	}
	uniqueIDs := make([]int64, 0, len(notificationIDs))
	seen := make(map[int64]struct{}, len(notificationIDs))
	for _, notificationID := range notificationIDs {
		if notificationID <= 0 {
			return errors.New("invalid inbox notification ID")
		}
		if _, exists := seen[notificationID]; exists {
			continue
		}
		seen[notificationID] = struct{}{}
		uniqueIDs = append(uniqueIDs, notificationID)
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ownedIDs []int64
		if err := tx.Model(&InboxNotification{}).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("agent_id = ? AND source_type = ? AND notification_id IN ?", agentID, source, uniqueIDs).
			Pluck("notification_id", &ownedIDs).Error; err != nil {
			return err
		}
		if len(ownedIDs) != len(uniqueIDs) {
			return gorm.ErrRecordNotFound
		}
		return tx.Model(&InboxNotification{}).
			Where("agent_id = ? AND source_type = ? AND notification_id IN ?", agentID, source, uniqueIDs).
			Where("acknowledged_at IS NULL").
			Update("acknowledged_at", acknowledgedAt).Error
	})
}
