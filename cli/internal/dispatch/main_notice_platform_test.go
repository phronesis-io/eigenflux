package dispatch

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestMainNoticePlatformAvailabilitySeparatesImplementationFromAcceptance(t *testing.T) {
	for _, tc := range []struct {
		host, platform, status, protocol string
		implemented                      bool
	}{
		{"codex", "darwin", "verified_macos", "codex_turn", true},
		{"codex", "windows", "implemented_untested", "codex_turn", true},
		{"codex", "linux", "unsupported_platform", "codex_turn", false},
		{"hermes", "windows", "untested", "acp", false},
		{"claude-code", "darwin", "untested", "acp", false},
		{"unknown", "windows", "untested", "acp", false},
	} {
		capability := mainNoticeCapabilityForPlatform(tc.host, tc.platform)
		if capability.Status != tc.status || capability.Protocol != tc.protocol || mainNoticeHostImplementedOn(tc.host, tc.platform) != tc.implemented {
			t.Fatalf("%s/%s: %+v", tc.host, tc.platform, capability)
		}
	}
	if MainNoticeHostVerified("codex") != (runtime.GOOS == "darwin") {
		t.Fatal("implementation was reported as real-host acceptance")
	}
}

func TestImplementedMainNoticeStillRequiresExactNativeBinding(t *testing.T) {
	binding := Binding{Host: "codex", Mode: "native", MainSessionID: noticeSession, Env: map[string]string{"CODEX_HOME": filepath.Join(t.TempDir(), "codex")}}
	if MainNoticeBindingEnabled(binding) != MainNoticeHostImplemented("codex") {
		t.Fatal("an implemented platform cannot test its bound main conversation")
	}
	for _, change := range []func(*Binding){
		func(b *Binding) { b.MainSessionID = "" },
		func(b *Binding) { b.Mode = "acp" },
		func(b *Binding) { b.Host = "hermes" },
		func(b *Binding) { b.Env = map[string]string{"CODEX_HOME": "relative"} },
	} {
		invalid := binding
		change(&invalid)
		if MainNoticeBindingEnabled(invalid) {
			t.Fatal("incomplete binding enabled", invalid.Host, invalid.Mode)
		}
	}
}
