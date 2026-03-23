package console

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"console.eigenflux.ai/internal/dal"
	"console.eigenflux.ai/internal/db"
	"console.eigenflux.ai/internal/idgen"
	"console.eigenflux.ai/internal/model"
	"console.eigenflux.ai/internal/notification"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

var notifIDGen idgen.IDGenerator

func InitNotificationService(gen idgen.IDGenerator) {
	notifIDGen = gen
}

func notifService() *notification.Service {
	return notification.NewService(db.DB, db.RDB)
}

// --- Response types ---

type SystemNotificationInfo struct {
	NotificationID     string `json:"notification_id"`
	Type               string `json:"type"`
	Content            string `json:"content"`
	Status             int32  `json:"status"`
	AudienceType       string `json:"audience_type"`
	AudienceExpression string `json:"audience_expression"`
	StartAt            int64  `json:"start_at"`
	EndAt              int64  `json:"end_at"`
	OfflineAt          int64  `json:"offline_at"`
	CreatedAt          int64  `json:"created_at"`
	UpdatedAt          int64  `json:"updated_at"`
}

type ListSystemNotificationsData struct {
	Notifications []SystemNotificationInfo `json:"notifications"`
	Total         int64                    `json:"total"`
	Page          int32                    `json:"page"`
	PageSize      int32                    `json:"page_size"`
}

type ListSystemNotificationsResp struct {
	Code int32                        `json:"code"`
	Msg  string                       `json:"msg"`
	Data *ListSystemNotificationsData `json:"data,omitempty"`
}

type SystemNotificationData struct {
	Notification SystemNotificationInfo `json:"notification"`
}

type SystemNotificationResp struct {
	Code int32                   `json:"code"`
	Msg  string                  `json:"msg"`
	Data *SystemNotificationData `json:"data,omitempty"`
}

type createSystemNotificationReq struct {
	Type    string `json:"type"`
	Content string `json:"content"`
	Status  *int32 `json:"status"`
	StartAt *int64 `json:"start_at"`
	EndAt   *int64 `json:"end_at"`
}

type updateSystemNotificationReq struct {
	Type    *string `json:"type"`
	Content *string `json:"content"`
	Status  *int32  `json:"status"`
	StartAt *int64  `json:"start_at"`
	EndAt   *int64  `json:"end_at"`
}

// ListSystemNotifications godoc
// @Summary      List system notifications
// @Description  Returns a paginated list of system notifications with optional status filter
// @Tags         console
// @Produce      json
// @Param        page       query  integer  false  "Page number (default: 1)"
// @Param        page_size  query  integer  false  "Items per page (default: 20, max: 100)"
// @Param        status     query  integer  false  "Filter by status (0=draft, 1=active, 2=offline)"
// @Success      200  {object}  ListSystemNotificationsResp
// @Router       /console/api/v1/system-notifications [get]
func ListSystemNotifications(ctx context.Context, c *app.RequestContext) {
	page, pageSize := parsePagination(c)

	var statusFilter *int32
	if raw := strings.TrimSpace(c.Query("status")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			writeConsoleError(c, "invalid status")
			return
		}
		v := int32(parsed)
		statusFilter = &v
	}

	rows, total, err := dal.ListSystemNotifications(db.DB, dal.ListSystemNotificationsParams{
		Page:     page,
		PageSize: pageSize,
		Status:   statusFilter,
	})
	if err != nil {
		writeConsoleError(c, "database query failed: "+err.Error())
		return
	}

	infos := make([]SystemNotificationInfo, 0, len(rows))
	for _, row := range rows {
		infos = append(infos, toSystemNotificationInfo(row))
	}

	c.JSON(consts.StatusOK, &ListSystemNotificationsResp{
		Code: 0,
		Msg:  "success",
		Data: &ListSystemNotificationsData{
			Notifications: infos,
			Total:         total,
			Page:          page,
			PageSize:      pageSize,
		},
	})
}

// CreateSystemNotification godoc
// @Summary      Create system notification
// @Description  Creates a new system notification
// @Tags         console
// @Accept       json
// @Produce      json
// @Param        body  body  createSystemNotificationReq  true  "Create request"
// @Success      200   {object}  SystemNotificationResp
// @Router       /console/api/v1/system-notifications [post]
func CreateSystemNotification(ctx context.Context, c *app.RequestContext) {
	var req createSystemNotificationReq
	if err := c.BindAndValidate(&req); err != nil {
		writeConsoleError(c, "invalid request: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Type) == "" {
		writeConsoleError(c, "type is required")
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeConsoleError(c, "content is required")
		return
	}
	if req.StartAt != nil && req.EndAt != nil && *req.StartAt > 0 && *req.EndAt > 0 && *req.EndAt <= *req.StartAt {
		writeConsoleError(c, "end_at must be greater than start_at")
		return
	}

	if notifIDGen == nil {
		writeConsoleError(c, "notification id generator not initialized")
		return
	}
	notifID, err := notifIDGen.NextID()
	if err != nil {
		writeConsoleError(c, "generate notification id failed: "+err.Error())
		return
	}

	var status int16
	if req.Status != nil {
		status = int16(*req.Status)
	}
	var startAt, endAt int64
	if req.StartAt != nil {
		startAt = *req.StartAt
	}
	if req.EndAt != nil {
		endAt = *req.EndAt
	}

	row, err := dal.CreateSystemNotification(ctx, db.DB, dal.CreateSystemNotificationParams{
		NotificationID: notifID,
		Type:           strings.TrimSpace(req.Type),
		Content:        strings.TrimSpace(req.Content),
		Status:         status,
		StartAt:        startAt,
		EndAt:          endAt,
	})
	if err != nil {
		writeConsoleError(c, "create failed: "+err.Error())
		return
	}

	if row.Status == model.StatusActive {
		svc := notifService()
		if putErr := svc.ActiveStore().Put(ctx, row); putErr != nil {
			writeConsoleError(c, "created but redis sync failed: "+putErr.Error())
			return
		}
	}

	c.JSON(consts.StatusOK, &SystemNotificationResp{
		Code: 0,
		Msg:  "success",
		Data: &SystemNotificationData{Notification: toSystemNotificationInfo(*row)},
	})
}

// UpdateSystemNotification godoc
// @Summary      Update system notification
// @Description  Updates fields of an existing system notification
// @Tags         console
// @Accept       json
// @Produce      json
// @Param        notification_id  path  integer  true  "Notification ID"
// @Param        body  body  updateSystemNotificationReq  true  "Update request"
// @Success      200   {object}  SystemNotificationResp
// @Router       /console/api/v1/system-notifications/{notification_id} [put]
func UpdateSystemNotification(ctx context.Context, c *app.RequestContext) {
	notifID, ok := parseNotificationID(c)
	if !ok {
		return
	}

	var req updateSystemNotificationReq
	if err := c.BindAndValidate(&req); err != nil {
		writeConsoleError(c, "invalid request: "+err.Error())
		return
	}
	if req.Type == nil && req.Content == nil && req.Status == nil && req.StartAt == nil && req.EndAt == nil {
		writeConsoleError(c, "at least one field must be provided")
		return
	}

	row, err := dal.UpdateSystemNotification(ctx, db.DB, notifID, dal.UpdateSystemNotificationParams{
		Type:    req.Type,
		Content: req.Content,
		Status:  req.Status,
		StartAt: req.StartAt,
		EndAt:   req.EndAt,
	})
	if err != nil {
		writeNotificationError(c, err)
		return
	}

	syncActiveStore(ctx, row)

	c.JSON(consts.StatusOK, &SystemNotificationResp{
		Code: 0,
		Msg:  "success",
		Data: &SystemNotificationData{Notification: toSystemNotificationInfo(*row)},
	})
}

// OfflineSystemNotification godoc
// @Summary      Offline system notification
// @Description  Sets a system notification to offline status
// @Tags         console
// @Produce      json
// @Param        notification_id  path  integer  true  "Notification ID"
// @Success      200  {object}  SystemNotificationResp
// @Router       /console/api/v1/system-notifications/{notification_id}/offline [post]
func OfflineSystemNotification(ctx context.Context, c *app.RequestContext) {
	notifID, ok := parseNotificationID(c)
	if !ok {
		return
	}

	row, err := dal.OfflineSystemNotification(ctx, db.DB, notifID)
	if err != nil {
		writeNotificationError(c, err)
		return
	}

	svc := notifService()
	_ = svc.ActiveStore().Remove(ctx, notifID)

	c.JSON(consts.StatusOK, &SystemNotificationResp{
		Code: 0,
		Msg:  "success",
		Data: &SystemNotificationData{Notification: toSystemNotificationInfo(*row)},
	})
}

func parseNotificationID(c *app.RequestContext) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("notification_id")), 10, 64)
	if err != nil || id <= 0 {
		writeConsoleError(c, "invalid notification_id")
		return 0, false
	}
	return id, true
}

func writeNotificationError(c *app.RequestContext, err error) {
	if errors.Is(err, dal.ErrNotificationNotFound) {
		writeConsoleError(c, err.Error())
		return
	}
	writeConsoleError(c, err.Error())
}

func syncActiveStore(ctx context.Context, row *model.SystemNotification) {
	svc := notifService()
	if row.Status == model.StatusActive && row.OfflineAt == 0 {
		_ = svc.ActiveStore().Put(ctx, row)
	} else {
		_ = svc.ActiveStore().Remove(ctx, row.NotificationID)
	}
}

func toSystemNotificationInfo(n model.SystemNotification) SystemNotificationInfo {
	return SystemNotificationInfo{
		NotificationID:     strconv.FormatInt(n.NotificationID, 10),
		Type:               n.Type,
		Content:            n.Content,
		Status:             int32(n.Status),
		AudienceType:       n.AudienceType,
		AudienceExpression: n.AudienceExpression,
		StartAt:            n.StartAt,
		EndAt:              n.EndAt,
		OfflineAt:          n.OfflineAt,
		CreatedAt:          n.CreatedAt,
		UpdatedAt:          n.UpdatedAt,
	}
}
