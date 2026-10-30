package desktopnotify

import (
	"context"
	"testing"
)

func TestAppleNotificationLanguage(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{"(\n    \"zh-Hans-CN\",\n    en\n)", "zh"},
		{`("zh-Hant-TW", "en-US")`, "zh"},
		{`(zh, en)`, "zh"},
		{`("en-US", "zh-Hans-CN")`, "en"},
		{`(fr, "zh-Hant")`, "en"},
		{`(ja)`, "en"},
		{`()`, "en"},
		{`zh-Hans-CN`, "en"},
		{`("zh-Hans-CN)`, "en"},
		{`(unknown, zh)`, "en"},
	} {
		if got := appleLanguage([]byte(tt.input), nil); got != tt.want {
			t.Fatalf("%q: got %s, want %s", tt.input, got, tt.want)
		}
	}
	if appleLanguage([]byte(`(zh)`), context.DeadlineExceeded) != "en" {
		t.Fatal("read timeout must use English")
	}
}
