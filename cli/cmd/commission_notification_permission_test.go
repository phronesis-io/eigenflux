package cmd

import (
	"bytes"
	"context"
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
