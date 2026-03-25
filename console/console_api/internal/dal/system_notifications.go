package dal

import (
	"context"
	"errors"
	"time"

	"console.eigenflux.ai/internal/model"

	"gorm.io/gorm"
)

var ErrNotificationNotFound = errors.New("system notification not found")

type ListSystemNotificationsParams struct {
	Page     int32
	PageSize int32
	Status   *int32
}

func ListSystemNotifications(gormDB *gorm.DB, params ListSystemNotificationsParams) ([]model.SystemNotification, int64, error) {
	var rows []model.SystemNotification
	var total int64

	query := gormDB.Model(&model.SystemNotification{})
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
	NotificationID     int64
	Type               string
	Content            string
	Status             int16
	StartAt            int64
	EndAt              int64
	AudienceExpression string
}

func CreateSystemNotification(ctx context.Context, gormDB *gorm.DB, params CreateSystemNotificationParams) (*model.SystemNotification, error) {
	now := time.Now().UnixMilli()
	row := &model.SystemNotification{
		NotificationID:     params.NotificationID,
		Type:               params.Type,
		Content:            params.Content,
		Status:             params.Status,
		AudienceType:       model.AudienceTypeBroadcast,
		AudienceExpression: params.AudienceExpression,
		StartAt:            params.StartAt,
		EndAt:              params.EndAt,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := gormDB.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

type UpdateSystemNotificationParams struct {
	Type               *string
	Content            *string
	Status             *int32
	StartAt            *int64
	EndAt              *int64
	AudienceExpression *string
}

func UpdateSystemNotification(ctx context.Context, gormDB *gorm.DB, notificationID int64, params UpdateSystemNotificationParams) (*model.SystemNotification, error) {
	var row model.SystemNotification
	if err := gormDB.WithContext(ctx).Take(&row, "notification_id = ?", notificationID).Error; err != nil {
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
	if params.AudienceExpression != nil {
		updates["audience_expression"] = *params.AudienceExpression
	}

	if err := gormDB.WithContext(ctx).
		Model(&model.SystemNotification{}).
		Where("notification_id = ?", notificationID).
		Updates(updates).Error; err != nil {
		return nil, err
	}

	if err := gormDB.WithContext(ctx).Take(&row, "notification_id = ?", notificationID).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func OfflineSystemNotification(ctx context.Context, gormDB *gorm.DB, notificationID int64) (*model.SystemNotification, error) {
	var row model.SystemNotification
	if err := gormDB.WithContext(ctx).Take(&row, "notification_id = ?", notificationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotificationNotFound
		}
		return nil, err
	}

	now := time.Now().UnixMilli()
	if err := gormDB.WithContext(ctx).
		Model(&model.SystemNotification{}).
		Where("notification_id = ?", notificationID).
		Updates(map[string]interface{}{
			"status":     model.StatusOffline,
			"offline_at": now,
			"updated_at": now,
		}).Error; err != nil {
		return nil, err
	}

	if err := gormDB.WithContext(ctx).Take(&row, "notification_id = ?", notificationID).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func GetSystemNotification(ctx context.Context, gormDB *gorm.DB, notificationID int64) (*model.SystemNotification, error) {
	var row model.SystemNotification
	if err := gormDB.WithContext(ctx).Take(&row, "notification_id = ?", notificationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotificationNotFound
		}
		return nil, err
	}
	return &row, nil
}

func ListActiveSystemNotifications(gormDB *gorm.DB) ([]model.SystemNotification, error) {
	var rows []model.SystemNotification
	err := gormDB.Where("status = ? AND offline_at = 0", model.StatusActive).Find(&rows).Error
	return rows, err
}
