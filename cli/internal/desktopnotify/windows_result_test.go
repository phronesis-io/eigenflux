package desktopnotify

import (
	"errors"
	"strings"
	"testing"
)

func TestWindowsHelperResultSeparatesStatusAndFailure(t *testing.T) {
	for _, tc := range []struct {
		output string
		err    error
		want   string
	}{
		{"accepted\r\n", nil, ""},
		{"permission_denied", errors.New("exit status 1"), ErrPermission.Error()},
		{"setup_required", errors.New("exit status 1"), "desktop notifications need initial setup"},
		{"setting_unavailable", errors.New("exit status 1"), errWindowsSettingUnavailable.Error()},
		{"notification_failed:setting_read:0x80004005", errors.New("exit status 1"), "Windows notification helper failed (stage=setting_read, HRESULT=0x80004005)"},
		{"notification_failed:toast_show:0x80070005", errors.New("exit status 1"), "Windows notification helper failed (stage=toast_show, HRESULT=0x80070005)"},
		{"accepted", errors.New("exit status 1"), "Windows notification helper failed (check PowerShell availability and policy): exit status 1"},
	} {
		err := windowsHelperResult([]byte(tc.output), tc.err)
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != tc.want {
			t.Fatalf("%q: got %q, want %q", tc.output, got, tc.want)
		}
		if tc.output == "setting_unavailable" && errors.Is(err, ErrPermission) {
			t.Fatal("unknown setting became a permission denial")
		}
	}
}

func TestWindowsHelperResultRejectsUntrustedDiagnostics(t *testing.T) {
	for _, output := range []string{
		"notification_failed:token=secret:0x80004005",
		"notification_failed:setting_read:token=secret",
		"notification_failed:setting_read:0x80004005:token=secret",
		"notification_failed:setting_read:0x80004005\nhttps://example.com/?token=secret",
		"private exception token=secret",
	} {
		err := windowsHelperResult([]byte(output), errors.New("exit status 1"))
		if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "example.com") {
			t.Fatalf("unsafe diagnostics: %v", err)
		}
	}
}
