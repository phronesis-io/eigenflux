package cmd

import (
	"testing"

	"cli.eigenflux.ai/internal/desktopnotify"
)

func TestNotificationTestMessageLanguages(t *testing.T) {
	for _, language := range []string{"zh", "en", "de", ""} {
		message := notificationTestMessage(language, "test:1", "https://example.com/page")
		wantTitle, wantBody := "EigenFlux notification test", "Click this notification to check that your browser opens the specified page."
		if language == "zh" {
			wantTitle, wantBody = "EigenFlux 通知测试", "点击这条通知，验证浏览器能否打开指定页面。"
		}
		if message.Title != wantTitle || message.Body != wantBody || message.ID != "test:1" || message.URL != "https://example.com/page" {
			t.Fatalf("%s: %+v", language, message)
		}
		if err := desktopnotify.Validate(message); err != nil {
			t.Fatal(err)
		}
	}
}
