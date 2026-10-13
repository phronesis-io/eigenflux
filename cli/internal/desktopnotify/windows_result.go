package desktopnotify

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var errWindowsSettingUnavailable = errors.New("Windows notification status unavailable (code=setting_unavailable); the helper could not read the OS setting, not an authorization denial")
var windowsHResult = regexp.MustCompile(`^0x[0-9A-Fa-f]{8}$`)

// Only fixed helper stages and numeric HRESULTs may enter diagnostics. Never
// echo PowerShell stderr, request data, URLs or arbitrary exception messages.
func windowsHelperResult(output []byte, runErr error) error {
	result := strings.TrimSpace(string(output))
	switch result {
	case "permission_denied":
		return ErrPermission
	case "setup_required":
		return errors.New("desktop notifications need initial setup")
	case "setting_unavailable":
		return errWindowsSettingUnavailable
	case "accepted":
		if runErr == nil {
			return nil
		}
	}
	parts := strings.Split(result, ":")
	if len(parts) == 3 && parts[0] == "notification_failed" && windowsHResult.MatchString(parts[2]) {
		switch parts[1] {
		case "request", "remove", "registration", "winrt_load", "notifier_create", "setting_read", "toast_xml", "toast_create", "toast_show":
			return fmt.Errorf("Windows notification helper failed (stage=%s, HRESULT=%s)", parts[1], parts[2])
		}
	}
	if runErr != nil {
		return fmt.Errorf("Windows notification helper failed (check PowerShell availability and policy): %w", runErr)
	}
	return errors.New("invalid Windows notification helper response")
}
