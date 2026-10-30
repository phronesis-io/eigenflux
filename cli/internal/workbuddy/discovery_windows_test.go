//go:build windows

package workbuddy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsSidecarDiscoveryDistinguishesDesktopReadiness(t *testing.T) {
	for _, tc := range []struct {
		name     string
		paths    []string
		probeErr error
		want     string
	}{
		{name: "desktop closed", want: "workbuddy_host_not_running:"},
		{name: "desktop running without lazy sidecar", paths: []string{`C:\WorkBuddy\WorkBuddy.exe`}, want: "workbuddy_sidecar_not_ready:"},
		{name: "process query failed", probeErr: errors.New("private diagnostic"), want: "workbuddy_host_status_unknown:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			_, err := readWindowsSidecarPID(filepath.Join(t.TempDir(), "sidecar.pid"), func() ([]string, error) {
				calls++
				return tc.paths, tc.probeErr
			})
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			if strings.Contains(err.Error(), "private diagnostic") || !strings.Contains(err.Error(), "existing task and binding unchanged") {
				t.Fatalf("unsafe or unactionable error: %v", err)
			}
		})
	}
}

func TestWindowsSidecarReadFailureDoesNotClaimHostStopped(t *testing.T) {
	_, err := readWindowsSidecarPID(t.TempDir(), func() ([]string, error) {
		t.Fatal("must not query processes for a non-missing-file failure")
		return nil, nil
	})
	if err == nil || !strings.HasPrefix(err.Error(), "workbuddy_host_pid_unreadable:") {
		t.Fatal(err)
	}
}

func TestWindowsSidecarExistingPIDDoesNotQueryDesktop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sidecar.pid")
	want := `{"controlPipeUuid":"0123abcd"}`
	if err := os.WriteFile(path, []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readWindowsSidecarPID(path, func() ([]string, error) {
		t.Fatal("existing Sidecar discovery does not depend on Desktop process enumeration")
		return nil, nil
	})
	if err != nil || string(got) != want {
		t.Fatalf("got=%q err=%v", got, err)
	}
}
