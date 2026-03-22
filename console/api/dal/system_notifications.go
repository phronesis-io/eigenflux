package dal

import (
	"context"
	"errors"
	"time"

	"eigenflux_server/pkg/notification"

	"gorm.io/gorm"
)

var ErrNotificationNotFound = errors.New("system notification not found")

type ListSystemNotificationsParams struct {
	Page     int32
	PageSize int32
	Status   *int32
}

func ListSystemNotifications(db *gorm.DB, params ListSystemNotificationsParams) ([]notification.SystemNotification, int64, error) {
	var rows []notification.SystemNotification
	var total int64

	query := db.Model(&notification.SystemNotification{})
	if params.Status != nil {
		query = query.Where("status = ?", *params.Status)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	offset := (params.Page - 1) * params.PageSize
	err := query.
		Order("created_at DESC, notification_id DESC").
		Offset(int(offset)).
		Limit(int(params.PageSize)).
		Find(&rows).Error
	return rows, total, err
}

type CreateSystemNotificationParams struct {
	NotificationID int64
	Type           string
	Content        string
	Status         int16
	StartAt        int64
	EndAt          int64
}

func CreateSystemNotification(ctx context.Context, db *gorm.DB, params CreateSystemNotificationParams) (*notification.SystemNotification, error) {
	now := time.Now().UnixMilli()
	row := &notification.SystemNotification{
		NotificationID:     params.NotificationID,
		Type:               params.Type,
		Content:            params.Content,
		Status:             params.Status,
		AudienceType:       notification.AudienceTypeBroadcast,
		AudienceExpression: "",
		StartAt:            params.StartAt,
		EndAt:              params.EndAt,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

type UpdateSystemNotificationParams struct {
	Type    *string
	Content *string
	Status  *int32
	StartAt *int64
	EndAt   *int64
}

func UpdateSystemNotification(ctx context.Context, db *gorm.DB, notificationID int64, params UpdateSystemNotificationParams) (*notification.SystemNotification, error) {
	var row notification.SystemNotification
	if err := db.WithContext(ctx).Take(&row, "notification_id = ?", notificationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotificationNotFound
		}
		return nil, err
	}

	updates := map[string]interface{}{
		"updated_at": time.Now().UnixMilli(),
	}
	if params.Type != nil {
		updates["type"] = *params.Type
	}
	if params.Content != nil {
		updates["content"] = *params.Content
	}
	if params.Status != nil {
		updates["status"] = *params.Status
	}
	if params.StartAt != nil {
		updates["start_at"] = *params.StartAt
	}
	if params.EndAt != nil {
		updates["end_at"] = *params.EndAt
	}

	if err := db.WithContext(ctx).
		Model(&notification.SystemNotification{}).
		Where("notification_id = ?", notificationID).
		Updates(updates).Error; err != nil {
		return nil, err
	}

	if err := db.WithContext(ctx).Take(&row, "notification_id = ?", notificationID).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func OfflineSystemNotification(ctx context.Context, db *gorm.DB, notificationID int64) (*notification.SystemNotification, error) {
	var row notification.SystemNotification
	if err := db.WithContext(ctx).Take(&row, "notification_id = ?", notificationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotificationNotFound
		}
		return nil, err
	}

	now := time.Now().UnixMilli()
	if err := db.WithContext(ctx).
		Model(&notification.SystemNotification{}).
		Where("notification_id = ?", notificationID).
		Updates(map[string]interface{}{
			"status":     notification.StatusOffline,
			"offline_at": now,
			"updated_at": now,
		}).Error; err != nil {
		return nil, err
	}

	if err := db.WithContext(ctx).Take(&row, "notification_id = ?", notificationID).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func GetSystemNotification(ctx context.Context, db *gorm.DB, notificationID int64) (*notification.SystemNotification, error) {
	var row notification.SystemNotification
	if err := db.WithContext(ctx).Take(&row, "notification_id = ?", notificationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotificationNotFound
		}
		return nil, err
	}
	return &row, nil
}

// ListActiveSystemNotifications returns all active, non-offline notifications.
func ListActiveSystemNotifications(db *gorm.DB) ([]notification.SystemNotification, error) {
	var rows []notification.SystemNotification
	err := db.Where("status = ? AND offline_at = 0", notification.StatusActive).Find(&rows).Error
	return rows, err
}
