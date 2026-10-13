package desktopnotify

import (
	"errors"
	"testing"
)

func TestPreferredNotificationLanguage(t *testing.T) {
	for _, language := range []string{"zh", "zh-CN", "zh-Hans-CN", "zh-Hant-TW", "zh-HK", "zh_MO", "ZH-sg"} {
		if got := preferredLanguage([]string{language, "en"}, nil); got != "zh" {
			t.Fatalf("%s: %s", language, got)
		}
	}
	for _, language := range []string{"en", "en-US", "fr-FR", "de", "ja-JP", "ko", "", "unknown", "zho"} {
		if got := preferredLanguage([]string{language, "zh-Hans"}, nil); got != "en" {
			t.Fatalf("%s: %s", language, got)
		}
	}
	if preferredLanguage(nil, nil) != "en" || preferredLanguage([]string{"zh"}, errors.New("unavailable")) != "en" {
		t.Fatal("missing preference or OS failure must use English")
	}
}
