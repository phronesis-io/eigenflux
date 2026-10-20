package dispatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const noticeSession = "01a0d200-9d41-7cd2-9214-3f04761910c4"
const noticePrompt = "Order 123 was delivered. Summarize the result without confirming receipt."

func noticeFixtureTarget(t *testing.T, host, scenario string) MainNoticeTarget {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	mode := "acp"
	if host == "codex" {
		mode = "native"
	}
	return MainNoticeTarget{Host: host, SessionID: noticeSession, HiddenInputEnabled: true, Binding: Binding{
		Host: host, Mode: mode, Home: t.TempDir(), Server: "test", WorkDir: t.TempDir(), TimeoutSeconds: 5,
		Command: []string{exe, "-test.run=^TestMainNoticeFixture$", "--"},
		Env:     map[string]string{"EF_MAIN_NOTICE_FIXTURE": scenario, "EF_MAIN_NOTICE_MARKER": filepath.Join(t.TempDir(), "calls")},
	}}
}

// Negative tests bind this child only to detect an unexpected process launch.
func TestMainNoticeFixture(t *testing.T) {
	if os.Getenv("EF_MAIN_NOTICE_FIXTURE") == "" {
		return
	}
	_ = os.WriteFile(os.Getenv("EF_MAIN_NOTICE_MARKER"), []byte("unexpected_process_start\n"), 0600)
	os.Exit(88)
}

func TestMainNoticeHiddenInputGateDoesNotStartProcess(t *testing.T) {
	for _, host := range []string{"codex", "hermes", "unknown"} {
		target := noticeFixtureTarget(t, host, "unused")
		target.HiddenInputEnabled = false
		result, err := (MainNoticeRunner{Target: target}).Run(context.Background(), noticePrompt)
		if !errors.Is(err, ErrMainNoticeUnsupported) || result != (Result{}) {
			t.Fatalf("%s: result %#v error %v", host, result, err)
		}
		if _, err := os.Stat(target.Binding.Env["EF_MAIN_NOTICE_MARKER"]); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s process started: %v", host, err)
		}
		capability := MainNoticeCapability(host)
		want := "acp"
		if host == "codex" {
			want = "codex_turn"
		}
		wantStatus := "acp_extension_required"
		if host == "codex" {
			wantStatus = "unsupported_platform"
			if runtime.GOOS == "darwin" {
				wantStatus = "verified_macos"
			}
			if runtime.GOOS == "windows" {
				wantStatus = "implemented_untested"
			}
		}
		if capability.Protocol != want || capability.Status != wantStatus {
			t.Fatalf("capability %#v", capability)
		}
	}
}

func TestMainNoticeRejectsMissingOrChangedTarget(t *testing.T) {
	for _, change := range []func(*MainNoticeTarget){
		func(target *MainNoticeTarget) { target.SessionID = "" },
		func(target *MainNoticeTarget) { target.Binding.Host = "other" },
		func(target *MainNoticeTarget) { target.Binding.MainSessionID = "other" },
		func(target *MainNoticeTarget) { target.SessionID = "--last" },
	} {
		target := noticeFixtureTarget(t, "codex", "unused")
		change(&target)
		if result, err := (MainNoticeRunner{Target: target}).Run(context.Background(), noticePrompt); err == nil || result != (Result{}) {
			t.Fatalf("result %#v error %v", result, err)
		}
		if _, err := os.Stat(target.Binding.Env["EF_MAIN_NOTICE_MARKER"]); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("process started: %v", err)
		}
	}
}
