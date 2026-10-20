package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/desktopnotify"
	"github.com/spf13/cobra"
)

func TestCommissionNotificationPermissionWarning(t *testing.T) {
	for _, tt := range []struct {
		name, platform, language, want, link string
		checkErr                             error
	}{
		{name: "ready", platform: "darwin", language: "zh"},
		{name: "cancelled", platform: "darwin", language: "zh", checkErr: context.Canceled},
		{name: "mac_denied", platform: "darwin", language: "zh", checkErr: fmt.Errorf("probe: %w", desktopnotify.ErrPermission), want: "重要提醒：这台电脑尚未开启", link: "x-apple.systempreferences:com.apple.Notifications-Settings.extension"},
		{name: "windows_denied", platform: "windows", language: "en", checkErr: desktopnotify.ErrPermission, want: "IMPORTANT: EigenFlux notifications are disabled", link: "ms-settings:notifications"},
		{name: "mac_first_use", platform: "darwin", language: "zh", checkErr: desktopnotify.ErrPermissionRequired, want: "尚未申请", link: "x-apple.systempreferences:com.apple.Notifications-Settings.extension"},
		{name: "windows_first_use_unknown", platform: "windows", language: "en", checkErr: desktopnotify.ErrPendingFirstNotification, want: "could not be confirmed", link: "ms-settings:notifications"},
		{name: "missing_helper", platform: "darwin", language: "en", checkErr: desktopnotify.ErrHelperMissing, want: "could not be confirmed", link: "x-apple.systempreferences:com.apple.Notifications-Settings.extension"},
		{name: "provider_unknown", platform: "windows", language: "zh", checkErr: errors.New("private-provider-data"), want: "暂时无法确认", link: "ms-settings:notifications"},
		{name: "other_language_english", platform: "windows", language: "fr", checkErr: desktopnotify.ErrPermission, want: "IMPORTANT:", link: "ms-settings:notifications"},
		{name: "timeout_unknown", platform: "darwin", language: "en", checkErr: context.DeadlineExceeded, want: "could not be confirmed", link: "x-apple.systempreferences:com.apple.Notifications-Settings.extension"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&out)
			cmd.SetErr(&diagnostic)
			cmd.SetContext(context.Background())
			checked := 0
			writeCommissionNotificationPermissionWarning(cmd, func(ctx context.Context) error {
				checked++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 10*time.Second || time.Until(deadline) < 9*time.Second {
					t.Fatalf("probe deadline is not bounded: %v %v", deadline, ok)
				}
				return tt.checkErr
			}, tt.platform, tt.language)
			if checked != 1 || out.Len() != 0 {
				t.Fatalf("expected one read-only check and unchanged stdout: checks=%d out=%q", checked, out.String())
			}
			text := diagnostic.String()
			if tt.want == "" {
				if text != "" {
					t.Fatalf("unexpected warning: %s", text)
				}
				return
			}
			if !strings.Contains(text, tt.want) || !strings.Contains(text, tt.link) || strings.Contains(text, "private-provider-data") {
				t.Fatalf("wrong or unsafe warning: %s", text)
			}
			if tt.checkErr == desktopnotify.ErrPermissionRequired && !strings.Contains(text, "eigenflux notify setup") {
				t.Fatal("first use has no setup guidance")
			}
			if tt.language == "zh" {
				if !strings.Contains(text, "点击后仍需本人开启") || !strings.Contains(text, "链接只打开当前电脑") {
					t.Fatalf("missing native settings boundary: %s", text)
				}
			} else if !strings.Contains(text, "Clicking does not confirm") || !strings.Contains(text, "this computer only") {
				t.Fatalf("missing native settings boundary: %s", text)
			}
		})
	}
}

func TestCommissionNotificationPermissionPreflight(t *testing.T) {
	for _, tt := range []struct {
		name, platform, status, code string
		checkErr                     error
		warning                      bool
	}{
		{name: "ready", platform: "darwin", status: "ready"},
		{name: "mac_denied", platform: "darwin", status: "permission_denied", code: "permission_denied", checkErr: desktopnotify.ErrPermission, warning: true},
		{name: "windows_unrequested", platform: "windows", status: "permission_required", code: "permission_required", checkErr: desktopnotify.ErrPermissionRequired, warning: true},
		{name: "windows_unknown", platform: "windows", status: "unknown", code: "pending_first_notification", checkErr: desktopnotify.ErrPendingFirstNotification, warning: true},
		{name: "helper_missing", platform: "darwin", status: "unknown", code: "helper_missing", checkErr: desktopnotify.ErrHelperMissing, warning: true},
		{name: "private_error", platform: "windows", status: "unknown", code: "check_failed", checkErr: errors.New("private-provider-data"), warning: true},
		{name: "timeout", platform: "darwin", status: "unknown", code: "check_timeout", checkErr: context.DeadlineExceeded, warning: true},
		{name: "cancelled", platform: "darwin", status: "unknown", code: "check_cancelled", checkErr: context.Canceled},
		{name: "unsupported", platform: "linux", status: "unsupported", code: "unsupported_platform", checkErr: desktopnotify.ErrUnsupported, warning: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&out)
			cmd.SetErr(&diagnostic)
			cmd.SetContext(context.Background())
			checked := 0
			err := writeCommissionNotificationPermissionPreflight(cmd, func(ctx context.Context) error {
				checked++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 10*time.Second || time.Until(deadline) < 9*time.Second {
					t.Fatalf("unbounded read-only check: %v %v", deadline, ok)
				}
				return tt.checkErr
			}, tt.platform, "zh")
			if err != nil || checked != 1 || diagnostic.Len() != 0 {
				t.Fatalf("preflight must check once and return only JSON: %v, %d, %q", err, checked, diagnostic.String())
			}
			var result commissionNotificationPermission
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Status != tt.status || result.Code != tt.code || (result.Warning != "") != tt.warning {
				t.Fatalf("incorrect permission fact: %+v", result)
			}
			if strings.Contains(out.String(), "private-provider-data") {
				t.Fatal("provider diagnostic leaked")
			}
			if tt.warning && tt.platform != "linux" && (result.SettingsURL == "" || result.SettingsPath == "") {
				t.Fatalf("warning has no local settings action: %+v", result)
			}
			if !tt.warning && (result.SettingsURL != "" || result.SettingsPath != "") {
				t.Fatal("ready or cancelled check unexpectedly prompted an action")
			}
		})
	}
}
