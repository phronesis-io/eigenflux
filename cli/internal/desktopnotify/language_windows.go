package desktopnotify

import "golang.org/x/sys/windows"

func platformLanguage() string {
	languages, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	return preferredLanguage(languages, err)
}
