package dispatch

import (
	"context"
	"os"
	"runtime"
	"testing"
)

// Opt-in, one existing-main turn only. The reply decision is read locally;
// no EigenFlux message API, credentials, files, or orders are used.
func TestCodexMainContentLocal(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("EIGENFLUX_TEST_CODEX_CONTENT") != "1" {
		t.Skip("explicit content-only Desktop acceptance is not enabled")
	}
	exe, id, home := os.Getenv("EIGENFLUX_TEST_CODEX_EXECUTABLE"), os.Getenv("EIGENFLUX_TEST_CODEX_SESSION"), os.Getenv("EIGENFLUX_TEST_CODEX_HOME")
	if exe == "" || id == "" || home == "" {
		t.Fatal("explicit executable, existing main thread and CODEX_HOME are required")
	}
	requestID, err := mainNoticeUUID()
	if err != nil {
		t.Fatal(err)
	}
	const body = "Windows 正文显示验收通过。"
	b := Binding{Mode: "native", Host: "codex", MainSessionID: id, Command: []string{exe}, WorkDir: t.TempDir(), Home: t.TempDir(), Server: "test", TimeoutSeconds: 90, Env: map[string]string{"CODEX_HOME": home}}
	prompt := "This is an owner-authorized local presentation fixture. Select reply with exactly this body: " + body + ". Do not use tools, send messages or change files/settings.\n## Output Contract\nReturn a JSON decision."
	prompt += codexPMDataMarker + `{"request_id":"` + requestID + `"}`
	got, err := (Runner{Binding: b}).Run(context.Background(), Request{ID: requestID, Kind: "pm_push", SessionID: id, Prompt: prompt})
	if err != nil {
		t.Fatalf("native content turn unconfirmed; never replay automatically: %v", err)
	}
	d, err := ParseDecision(got.Text, requestID)
	if err != nil || d.Action != "reply" || d.ReplyText != body || got.SessionID != id || got.RunID == "" {
		t.Fatalf("content-only native result not confirmed: %+v %v", got, err)
	}
	t.Logf("content-only final confirmed: session=%s turn=%s request=%s body=%s", got.SessionID, got.RunID, requestID, body)
}
