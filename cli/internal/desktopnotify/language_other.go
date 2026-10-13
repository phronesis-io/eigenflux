//go:build !darwin && !windows

package desktopnotify

func platformLanguage() string { return "en" }
