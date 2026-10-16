package consolev2

import (
	"context"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

const notificationDeviceFreshness = 10 * time.Minute

type notificationDevice struct {
	DeviceID        string `json:"device_id"`
	DeviceName      string `json:"device_name"`
	Platform        string `json:"platform"`
	PermissionState string `json:"permission_state"`
	ObservedAt      int64  `json:"observed_at"`
	Stale           bool   `json:"stale"`
}

type notificationDeviceRow struct {
	DeviceID   string
	DeviceName string
	Platform   string
	Result     string
	ErrorCode  string
	EventAt    int64
}

// Observations remain account scoped. An older upload cannot replace a newer
// check, and a successful send is not evidence of current OS permission.
const notificationDevicesQuery = `SELECT device_id, device_name, platform, result, error_code, event_at FROM (
 SELECT DISTINCT ON (properties->'notification'->>'device_id')
 properties->'notification'->>'device_id' AS device_id,
 properties->'notification'->>'device_name' AS device_name,
 properties->>'platform' AS platform, properties->>'result' AS result,
 properties->>'error_code' AS error_code, event_at
 FROM telemetry_events_v2
 WHERE agent_id = ? AND event_type = 'maintenance_attempt'
 AND event_at >= ? AND expires_at > ?
 AND properties->>'phase' = 'notification' AND properties->>'component' = 'notifier'
 AND properties->'notification'->>'action' = 'status'
 AND properties->'notification'->>'device_id' ~ '^[a-f0-9]{32}$'
 ORDER BY properties->'notification'->>'device_id', event_at DESC, created_at DESC, event_id DESC
) latest ORDER BY event_at DESC, device_id LIMIT 50`

func projectNotificationDevice(row notificationDeviceRow, now time.Time) notificationDevice {
	d := notificationDevice{DeviceID: row.DeviceID, DeviceName: row.DeviceName, Platform: row.Platform, ObservedAt: row.EventAt, PermissionState: "unknown"}
	d.Stale = row.EventAt < now.Add(-notificationDeviceFreshness).UnixMilli() || row.EventAt > now.Add(time.Minute).UnixMilli()
	if d.Stale {
		return d
	}
	switch {
	case row.Result == "ready" && row.ErrorCode == "":
		d.PermissionState = "ready"
	case row.Result == "disabled" && row.ErrorCode == "permission_denied":
		d.PermissionState = "denied"
	case row.Result == "failed" && row.ErrorCode == "permission_required":
		d.PermissionState = "not_requested"
	case row.Result == "failed":
		d.PermissionState = "unavailable"
	}
	return d
}

func (s *Service) listNotificationDevices(ctx context.Context, c *app.RequestContext) {
	id, ok := agentID(c)
	if !ok || id <= 0 {
		fail(c, http.StatusUnauthorized, "AGENT_REQUIRED", "Console authentication is required", nil)
		return
	}
	now := time.Now()
	var rows []notificationDeviceRow
	if s.db == nil || s.db.WithContext(ctx).Raw(notificationDevicesQuery, id, now.Add(-7*24*time.Hour).UnixMilli(), now.UnixMilli()).Scan(&rows).Error != nil {
		fail(c, http.StatusServiceUnavailable, "NOTIFICATION_DEVICES_UNAVAILABLE", "Notification device status is temporarily unavailable", nil)
		return
	}
	devices := make([]notificationDevice, 0, len(rows))
	for _, row := range rows {
		if !notificationDeviceID.MatchString(row.DeviceID) || !validNotificationDeviceName(row.DeviceName) || !maintenanceOneOf(row.Platform, "darwin", "windows") {
			continue
		}
		devices = append(devices, projectNotificationDevice(row, now))
	}
	c.Header("Cache-Control", "no-store")
	reply(c, http.StatusOK, map[string]interface{}{"devices": devices, "checked_at": now.UnixMilli()})
}
