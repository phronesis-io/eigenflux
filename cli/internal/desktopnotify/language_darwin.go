package desktopnotify

import (
	"context"
	"os/exec"
	"regexp"
	"time"
)

// defaults prints AppleLanguages as an OpenStep array; only its first item
// controls notification copy, even when a later preference is Chinese.
var firstAppleLanguage = regexp.MustCompile(`(?s)^\s*\(\s*(?:"([A-Za-z]{2,3}(?:[-_][A-Za-z0-9]{2,8})*)"|([A-Za-z]{2,3}(?:[-_][A-Za-z0-9]{2,8})*))\s*[,)]`)

func platformLanguage() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/bin/defaults", "read", "-g", "AppleLanguages").Output()
	return appleLanguage(output, err)
}

func appleLanguage(output []byte, err error) string {
	match := firstAppleLanguage.FindSubmatch(output)
	if err != nil || len(match) != 3 {
		return "en"
	}
	return preferredLanguage([]string{string(match[1]) + string(match[2])}, nil)
}
