package cmd

import (
	"encoding/json"
	"fmt"
	"time"

	"cli.eigenflux.ai/internal/desktopnotify"
	"github.com/spf13/cobra"
)

func newNotifyCommand() *cobra.Command {
	root := &cobra.Command{Use: "notify", Short: "Manage local clickable desktop notifications"}
	setup := &cobra.Command{Use: "setup", Short: "Install the local notification integration and request OS permission", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if err := desktopnotify.New().Setup(cmd.Context()); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"status": "ready"})
	}}
	status := &cobra.Command{Use: "status", Short: "Check local OS notification integration and permission", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if err := desktopnotify.New().Check(cmd.Context()); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"status": "ready", "delivery": "OS submission does not confirm banner visibility"})
	}}
	test := &cobra.Command{Use: "test", Short: "Send a test notification that opens a specified safe URL", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		target, _ := cmd.Flags().GetString("url")
		message := notificationTestMessage(desktopnotify.SystemLanguage(), fmt.Sprintf("test:%d", time.Now().UnixNano()), target)
		if err := desktopnotify.Validate(message); err != nil {
			return err
		}
		notifier := desktopnotify.New()
		if err := notifier.Setup(cmd.Context()); err != nil {
			return err
		}
		if err := notifier.Show(cmd.Context(), message); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"status": "submitted", "url": target})
	}}
	test.Flags().String("url", "", "HTTPS target; loopback HTTP is allowed for local testing")
	_ = test.MarkFlagRequired("url")
	root.AddCommand(setup, status, test)
	return root
}

func notificationTestMessage(language, id, target string) desktopnotify.Message {
	title, body := "EigenFlux notification test", "Click this notification to check that your browser opens the specified page."
	if language == "zh" {
		title, body = "EigenFlux 通知测试", "点击这条通知，验证浏览器能否打开指定页面。"
	}
	return desktopnotify.Message{ID: id, Title: title, Body: body, URL: target}
}

func init() { rootCmd.AddCommand(newNotifyCommand()) }
