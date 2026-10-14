package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/desktopnotify"
	"github.com/spf13/cobra"
)

func newNotifyCommand() *cobra.Command {
	root := &cobra.Command{Use: "notify", Short: "Manage local clickable desktop notifications"}
	setup := &cobra.Command{Use: "setup", Short: "Set up local notifications (macOS permission prompt; Windows sender registration)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		return writeNotificationStatus(cmd, observedNotificationCLI().Setup(cmd.Context()))
	}}
	status := &cobra.Command{Use: "status", Short: "Check local OS notification integration and permission", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		return writeNotificationStatus(cmd, observedNotificationCLI().Check(cmd.Context()))
	}}
	test := &cobra.Command{Use: "test", Short: "Send a test notification that opens a specified safe URL", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		target, _ := cmd.Flags().GetString("url")
		message := notificationTestMessage(desktopnotify.SystemLanguage(), fmt.Sprintf("test:%d", time.Now().UnixNano()), target)
		if err := desktopnotify.Validate(message); err != nil {
			return err
		}
		notifier := observedNotificationCLI()
		if err := notifier.Setup(cmd.Context()); err != nil && !errors.Is(err, desktopnotify.ErrPendingFirstNotification) {
			return err
		}
		if err := notifier.Show(cmd.Context(), message); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"status": "submitted", "url": target})
	}}
	test.Flags().String("url", "", "HTTPS target; loopback HTTP is allowed for local testing")
	_ = test.MarkFlagRequired("url")
	log := &cobra.Command{Use: "log", Short: "Read local notification observations (submission does not prove visibility)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		events, err := readNotificationObservations(config.HomeDir(), notificationServer())
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(events)
	}}
	root.AddCommand(setup, status, test, log)
	return root
}

func writeNotificationStatus(cmd *cobra.Command, err error) error {
	status := "ready"
	code := ""
	if errors.Is(err, desktopnotify.ErrPendingFirstNotification) {
		status, code = "registered", "pending_first_notification"
	} else if err != nil {
		return err
	}
	data := map[string]string{"status": status, "visibility": "unknown", "delivery": "OS submission does not confirm banner visibility"}
	if code != "" {
		data["code"] = code
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(data)
}

func notificationTestMessage(language, id, target string) desktopnotify.Message {
	title, body := "EigenFlux notification test", "Click this notification to check that the page opens."
	if language == "zh" {
		title, body = "EigenFlux 通知测试", "点击这条通知，看看能否正常打开页面。"
	}
	return desktopnotify.Message{ID: id, Title: title, Body: body, URL: target}
}

func init() { rootCmd.AddCommand(newNotifyCommand()) }
