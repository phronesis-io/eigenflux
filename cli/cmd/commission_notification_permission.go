package cmd

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"cli.eigenflux.ai/internal/desktopnotify"
	"github.com/spf13/cobra"
)

func warnCommissionNotificationPermission(cmd *cobra.Command) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		return
	}
	writeCommissionNotificationPermissionWarning(cmd, desktopnotify.New().Check, runtime.GOOS, desktopnotify.SystemLanguage())
}

// A local, read-only probe must not request permission or change the publish
// result. Keep guidance on stderr so the mutation response remains machine-safe.
func writeCommissionNotificationPermissionWarning(cmd *cobra.Command, check func(context.Context) error, platform, language string) {
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()
	err := check(ctx)
	if err == nil || errors.Is(err, context.Canceled) {
		return
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
	var message string
	switch {
	case errors.Is(err, desktopnotify.ErrPermission):
		message = "IMPORTANT: EigenFlux notifications are disabled on this computer. Enable them before publishing so you can receive order, payment and delivery alerts."
		if zh {
			message = "重要提醒：这台电脑尚未开启 EigenFlux 通知。请在上架前开启，以免错过接单、付款和交付提醒。"
		}
	case errors.Is(err, desktopnotify.ErrPermissionRequired):
		message = "IMPORTANT: EigenFlux notification permission has not been requested on this computer. Run eigenflux notify setup, then enable notifications before publishing so you do not miss order alerts."
		if zh {
			message = "重要提醒：这台电脑尚未申请 EigenFlux 通知权限。请先运行 eigenflux notify setup，再开启通知，以免上架后错过订单提醒。"
		}
	default:
		message = "Notice: EigenFlux notification permission could not be confirmed on this computer. Check the notification component and system settings; the current state does not prove that notifications are disabled."
		if zh {
			message = "提醒：暂时无法确认这台电脑的 EigenFlux 通知权限。请检查通知组件和系统设置；当前状态不代表通知已关闭。"
		}
	}
	fmt.Fprintln(cmd.ErrOrStderr(), message)
	if settingsURL == "" {
		return
	}
	if zh {
		if platform == "darwin" {
			path = "系统设置 → 通知 → EigenFlux → 允许通知"
		} else {
			path = "设置 → 系统 → 通知（或“通知和操作”）→ EigenFlux"
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "[打开这台电脑的通知设置](%s)\n无法跳转时，请手动打开：%s。链接只打开当前电脑；点击后仍需本人开启，CLI 不会把点击当作开启成功。\n", settingsURL, path)
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "[Open notification settings on this computer](%s)\nIf the link does not open, navigate manually: %s. The link opens this computer only; enable permission yourself. Clicking does not confirm that permission is enabled.\n", settingsURL, path)
}
