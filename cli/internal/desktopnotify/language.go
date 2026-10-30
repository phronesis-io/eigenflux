package desktopnotify

import (
	"strings"
	"sync"
)

var systemLanguage = sync.OnceValue(platformLanguage)

// SystemLanguage follows the current user's first OS UI language. Chinese
// variants use Simplified Chinese; all other languages and failures use English.
// Restart the process to observe changed preferences; queued messages keep copy.
func SystemLanguage() string { return systemLanguage() }

func preferredLanguage(languages []string, err error) string {
	if err != nil || len(languages) == 0 {
		return "en"
	}
	first := strings.ToLower(strings.TrimSpace(languages[0]))
	if first == "zh" || strings.HasPrefix(first, "zh-") || strings.HasPrefix(first, "zh_") {
		return "zh"
	}
	return "en"
}
