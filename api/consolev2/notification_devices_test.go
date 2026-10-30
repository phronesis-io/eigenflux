package consolev2

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

func TestNotificationDeviceProjection(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		result, code, want string
	}{
		{"ready", "", "ready"},
		{"disabled", "permission_denied", "denied"},
		{"failed", "permission_required", "not_requested"},
		{"registered", "pending_first_notification", "unknown"},
		{"failed", "helper_missing", "unavailable"},
		{"submitted", "", "unknown"},
	} {
		row := notificationDeviceRow{DeviceID: strings.Repeat("a", 32), DeviceName: "我的电脑", Platform: "windows", CLIVersion: "0.7.7", Result: tc.result, ErrorCode: tc.code, EventAt: now.UnixMilli()}
		got := projectNotificationDevice(row, now)
		if got.PermissionState != tc.want || got.Stale || got.DeviceName != row.DeviceName || got.CLIVersion != "0.7.7" {
			t.Fatalf("%+v: %+v", tc, got)
		}
		for _, at := range []time.Time{now.Add(-11 * time.Minute), now.Add(2 * time.Minute)} {
			row.EventAt = at.UnixMilli()
			got = projectNotificationDevice(row, now)
			if !got.Stale || got.PermissionState != "unknown" {
				t.Fatalf("stale or clock-skewed observation counted as current: %+v", got)
			}
		}
	}
}

func TestNotificationDeviceMetadataValidation(t *testing.T) {
	e := validNotificationMaintenance()
	e.Notification.Action, e.Notification.IDHash, e.Result = "status", "", "ready"
	e.Notification.DeviceID, e.Notification.DeviceName = strings.Repeat("a", 32), "办公室 Windows"
	if err := validateMaintenanceEvent(e, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", " leading", "trailing ", "line\nbreak", "hidden\u202e", "line\u2028break", strings.Repeat("字", 65), string([]byte{0xff})} {
		copy := e
		n := *e.Notification
		copy.Notification = &n
		n.DeviceName = name
		if validateMaintenanceEvent(copy, time.Now()) == nil {
			t.Fatalf("unsafe name accepted: %q", name)
		}
	}
	e.Notification.DeviceID = "hardware serial"
	if validateMaintenanceEvent(e, time.Now()) == nil {
		t.Fatal("arbitrary device identity accepted")
	}
	e.Notification.DeviceID = strings.Repeat("a", 32)
	e.Notification.Action = "setup"
	if validateMaintenanceEvent(e, time.Now()) == nil {
		t.Fatal("setup accepted as current permission observation")
	}
}

func TestNotificationDeviceEndpointRequiresIdentityAndFailsClosed(t *testing.T) {
	svc := &Service{}
	h := server.New()
	h.GET("/missing", svc.listNotificationDevices)
	h.GET("/unavailable", func(_ context.Context, c *app.RequestContext) { c.Set("agent_id", int64(42)) }, svc.listNotificationDevices)
	for _, tc := range []struct {
		path   string
		status int
	}{{"/missing", 401}, {"/unavailable", 503}} {
		status, _, _ := performJSON(t, h, "GET", tc.path, nil)
		if status != tc.status {
			t.Fatalf("%s: %d", tc.path, status)
		}
	}
}
