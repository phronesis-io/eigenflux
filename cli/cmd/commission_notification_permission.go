package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"time"

	"cli.eigenflux.ai/internal/desktopnotify"
	"github.com/spf13/cobra"
)

type commissionNotificationPermission struct {
	Status       string `json:"status"`
	Code         string `json:"code,omitempty"`
	Warning      string `json:"warning,omitempty"`
	SettingsURL  string `json:"settings_url,omitempty"`
	SettingsPath string `json:"settings_path,omitempty"`
}

func warnCommissionNotificationPermission(cmd *cobra.Command) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		return
	}
	writeCommissionNotificationPermissionWarning(cmd, desktopnotify.New().Check, runtime.GOOS, desktopnotify.SystemLanguage())
}

// A local, read-only probe must not request permission or change the publish
// result. Keep guidance on stderr so the mutation response remains machine-safe.
func writeCommissionNotificationPermissionWarning(cmd *cobra.Command, check func(context.Context) error, platform, language string) {
	result := checkCommissionNotificationPermission(cmd.Context(), check, platform, language)
	if result.Warning == "" {
		return
	}
	fmt.Fprintln(cmd.ErrOrStderr(), result.Warning)
	if result.SettingsURL == "" {
		return
	}
	if language == "zh" {
		fmt.Fprintf(cmd.ErrOrStderr(), "[打开这台电脑的通知设置](%s)\n无法跳转时，请手动打开：%s。链接只打开当前电脑；点击后仍需本人开启，CLI 不会把点击当作开启成功。\n", result.SettingsURL, result.SettingsPath)
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "[Open notification settings on this computer](%s)\nIf the link does not open, navigate manually: %s. The link opens this computer only; enable permission yourself. Clicking does not confirm that permission is enabled.\n", result.SettingsURL, result.SettingsPath)
}

// This status mode is independent of publish and has no observer or telemetry.
// Agents can show its guidance before asking for publication approval.
func writeCommissionNotificationPermissionPreflight(cmd *cobra.Command, check func(context.Context) error, platform, language string) error {
	return json.NewEncoder(cmd.OutOrStdout()).Encode(checkCommissionNotificationPermission(cmd.Context(), check, platform, language))
}

func checkCommissionNotificationPermission(parent context.Context, check func(context.Context) error, platform, language string) commissionNotificationPermission {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	err := check(ctx)
	if err == nil {
		return commissionNotificationPermission{Status: "ready"}
	}
	if errors.Is(err, context.Canceled) {
		return commissionNotificationPermission{Status: "unknown", Code: "check_cancelled"}
	}
	settingsURL, path := "", ""
	switch platform {
	case "darwin":
		settingsURL = "x-apple.systempreferences:com.apple.Notifications-Settings.extension"
		path = "System Settings → Notifications → EigenFlux → Allow notifications"
	case "windows":
		settingsURL = "ms-settings:notifications"
		path = "Settings → System → Notifications (or Notifications & actions) → EigenFlux"
	}
	zh := language == "zh"
	status, code, message := "unknown", "check_failed", ""
	switch {
	case errors.Is(err, desktopnotify.ErrPermission):
		status, code = "permission_denied", "permission_denied"
		message = "IMPORTANT: EigenFlux notifications are disabled on this computer. Enable them before publishing so you can receive order, payment and delivery alerts."
		if zh {
			message = "重要提醒：这台电脑尚未开启 EigenFlux 通知。请在上架前开启，以免错过接单、付款和交付提醒。"
		}
	case errors.Is(err, desktopnotify.ErrPermissionRequired):
		status, code = "permission_required", "permission_required"
		message = "IMPORTANT: EigenFlux notification permission has not been requested on this computer. Run eigenflux notify setup, then enable notifications before publishing so you do not miss order alerts."
		if zh {
			message = "重要提醒：这台电脑尚未申请 EigenFlux 通知权限。请先运行 eigenflux notify setup，再开启通知，以免上架后错过订单提醒。"
		}
	default:
		switch {
		case errors.Is(err, desktopnotify.ErrPendingFirstNotification):
			code = "pending_first_notification"
		case errors.Is(err, desktopnotify.ErrHelperMissing):
			code = "helper_missing"
		case errors.Is(err, desktopnotify.ErrUnsupported):
			status, code = "unsupported", "unsupported_platform"
		case errors.Is(err, context.DeadlineExceeded):
			code = "check_timeout"
		}
		message = "Notice: EigenFlux notification permission could not be confirmed on this computer. Check the notification component and system settings; the current state does not prove that notifications are disabled."
		if zh {
			message = "提醒：暂时无法确认这台电脑的 EigenFlux 通知权限。请检查通知组件和系统设置；当前状态不代表通知已关闭。"
		}
	}
	if zh {
		if platform == "darwin" {
			path = "系统设置 → 通知 → EigenFlux → 允许通知"
		} else if platform == "windows" {
			path = "设置 → 系统 → 通知（或“通知和操作”）→ EigenFlux"
		}
	}
	return commissionNotificationPermission{Status: status, Code: code, Warning: message, SettingsURL: settingsURL, SettingsPath: path}
}
