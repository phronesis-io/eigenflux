package dispatch

import (
	"context"
	"os"
	"testing"
)

// Opt-in, one existing-main presentation only.
// no EigenFlux message API, credentials, files, or orders are used.
func TestCodexMainContentLocal(t *testing.T) {
	if os.Getenv("EIGENFLUX_TEST_CODEX_CONTENT") != "1" {
		t.Skip("explicit content-only Desktop acceptance is not enabled")
	}
	exe, id, home := os.Getenv("EIGENFLUX_TEST_CODEX_EXECUTABLE"), os.Getenv("EIGENFLUX_TEST_CODEX_SESSION"), os.Getenv("EIGENFLUX_TEST_CODEX_HOME")
	if exe == "" || id == "" || home == "" {
		t.Fatal("explicit executable, existing main thread and CODEX_HOME are required")
	}
	const body = "EigenFlux 私信提醒：请在周四前确认材料。"
	b := Binding{Mode: "native", Host: "codex", MainSessionID: id, Command: []string{exe}, WorkDir: t.TempDir(), Home: t.TempDir(), Server: "test", TimeoutSeconds: 90, Env: map[string]string{"CODEX_HOME": home}}
	target := MainNoticeTarget{Host: b.Host, SessionID: id, Binding: b, HiddenInputEnabled: MainNoticeBindingEnabled(b)}
	prompt := "Present this authorized test summary verbatim. Use no tools or commentary." + PMOwnerNoticeMarker + `{"summary":"` + body + `"}`
	got, err := (MainNoticeRunner{Target: target}).Run(context.Background(), prompt)
	if err != nil || got.Text != body || got.SessionID != id || got.RunID == "" {
		t.Fatalf("native owner notice unconfirmed: %+v %v", got, err)
	}
}
