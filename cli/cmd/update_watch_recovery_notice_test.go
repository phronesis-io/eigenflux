package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"github.com/spf13/cobra"
)

func TestWatchRecoveryNoticeTransitions(t *testing.T) {
	cfg, _ := runtimeTestConfig(t, "https://notice.example.invalid", true)
	cmd := &cobra.Command{}
	var diagnostics bytes.Buffer
	cmd.SetErr(&diagnostics)
	base := updateWatchRecovery{Status: "pending", Reason: "external_loop_unverified", Action: "verify the existing scheduler"}
	check := func(r updateWatchRecovery, want string) {
		t.Helper()
		before := r
		annotateWatchRecoveryNotice(cmd, cfg, &r)
		if r.Notification != want {
			t.Fatalf("notification=%q want=%q result=%+v", r.Notification, want, r)
		}
		r.Notification = before.Notification
		if r != before {
			t.Fatalf("notice changed recovery result: before=%+v after=%+v", before, r)
		}
	}
	check(base, "required")
	check(base, "unchanged")
	changed := base
	changed.Status = "blocked"
	check(changed, "required")
	check(changed, "unchanged")
	changed.Reason = "host_managed_unverified"
	check(changed, "required")
	changed.Action = "verify the existing plugin"
	check(changed, "required")
	check(changed, "unchanged")
	check(updateWatchRecovery{Status: "blocked", Reason: "identity_changed"}, "")
	check(updateWatchRecovery{Status: "skipped", Reason: "owner_disabled"}, "")
	check(changed, "unchanged")
	check(updateWatchRecovery{Status: "ready", RuntimeReady: true}, "recovered")
	check(updateWatchRecovery{Status: "ready", RuntimeReady: true}, "")
	check(changed, "required")
	check(changed, "unchanged")
	if diagnostics.Len() != 0 {
		t.Fatalf("normal transitions emitted diagnostics: %s", &diagnostics)
	}
}

func TestWatchRecoveryNoticeIdentityIsolation(t *testing.T) {
	cfg, server := runtimeTestConfig(t, "https://first.example.invalid", true)
	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	base := updateWatchRecovery{Status: "pending", Reason: "external_loop_unverified", Action: "verify existing scheduler"}
	check := func(want string) {
		t.Helper()
		r := base
		annotateWatchRecoveryNotice(cmd, cfg, &r)
		if r.Notification != want {
			t.Fatalf("notification=%q want=%q result=%+v", r.Notification, want, r)
		}
	}
	check("required")
	check("unchanged")
	original, err := auth.LoadV2Credentials(server)
	if err != nil {
		t.Fatal(err)
	}
	for _, credentials := range []auth.V2Credentials{
		{AgentID: "agent-2", PrincipalID: "principal-2"},
		{AgentID: original.AgentID, PrincipalID: "new-principal"},
	} {
		credentials.AccessToken, credentials.RefreshToken = "test-access", "test-refresh"
		if err := auth.SaveV2Credentials(server, &credentials); err != nil {
			t.Fatal(err)
		}
		check("required")
		check("unchanged")
	}
	if err := auth.SaveV2Credentials(server, original); err != nil {
		t.Fatal(err)
	}
	check("unchanged")
	if err := cfg.UpdateServer(server, "https://second.example.invalid", ""); err != nil {
		t.Fatal(err)
	}
	check("required")
	check("unchanged")
	if err := cfg.UpdateServer(server, "https://first.example.invalid", ""); err != nil {
		t.Fatal(err)
	}
	check("unchanged")
	if err := cfg.AddServer("other-notice-server", "https://first.example.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := auth.LoadOrCreateIdentity("other-notice-server"); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveV2Credentials("other-notice-server", original); err != nil {
		t.Fatal(err)
	}
	serverFlag = "other-notice-server"
	check("required")
	check("unchanged")
	serverFlag = ""
	check("unchanged")
	otherHome := t.TempDir()
	restore := config.UseHomeDir(otherHome)
	if err := cfg.Save(); err != nil {
		restore()
		t.Fatal(err)
	}
	if _, _, _, err := auth.LoadOrCreateIdentity(server); err != nil {
		restore()
		t.Fatal(err)
	}
	if err := auth.SaveV2Credentials(server, original); err != nil {
		restore()
		t.Fatal(err)
	}
	check("required")
	check("unchanged")
	restore()
	check("unchanged")
}

func TestWatchRecoveryNoticeFailuresRemainActionableAndPrivate(t *testing.T) {
	cfg, server := runtimeTestConfig(t, "https://private-endpoint.example.invalid", true)
	cmd := &cobra.Command{}
	var diagnostics bytes.Buffer
	cmd.SetErr(&diagnostics)
	base := updateWatchRecovery{Status: "pending", Reason: "private-reason", Action: "private action /owner/private-path"}
	annotateWatchRecoveryNotice(cmd, cfg, &base)
	if base.Notification != "required" {
		t.Fatalf("first notice suppressed: %+v", base)
	}
	path, scope, err := watchRecoveryNoticeScope(cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state watchRecoveryNotice
	if err := json.Unmarshal(raw, &state); err != nil || state.Scope != scope || !state.Active {
		t.Fatalf("missing durable notice: %+v %v", state, err)
	}
	for _, private := range []string{config.HomeDir(), "private-endpoint", base.Reason, base.Action, "agent-1", "test-v2", "test-refresh"} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatalf("notice state exposed private data %q", private)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("notice must be owner-only: %v %v", info, err)
		}
	}
	if err := os.WriteFile(path, []byte("private malformed state"), 0600); err != nil {
		t.Fatal(err)
	}
	r := base
	annotateWatchRecoveryNotice(cmd, cfg, &r)
	if r.Notification != "required" || !strings.Contains(diagnostics.String(), "state_read_failed") {
		t.Fatalf("corrupt receipt suppressed notice: %+v %s", r, &diagnostics)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		diagnostics.Reset()
		r = base
		annotateWatchRecoveryNotice(cmd, cfg, &r)
		if r.Notification != "required" || !strings.Contains(diagnostics.String(), "state_write_failed") {
			t.Fatalf("failed persistence suppressed notice: %+v %s", r, &diagnostics)
		}
		if strings.Contains(diagnostics.String(), config.HomeDir()) || strings.Contains(diagnostics.String(), "private") || diagnostics.Len() > 200 {
			t.Fatalf("unbounded or private diagnostic: %s", &diagnostics)
		}
	}
	credentialsPath := filepath.Join(config.HomeDir(), "servers", server, "agent-v2-credentials.json")
	if err := os.Remove(credentialsPath); err != nil {
		t.Fatal(err)
	}
	diagnostics.Reset()
	r = base
	annotateWatchRecoveryNotice(cmd, cfg, &r)
	if r.Notification != "required" || !strings.Contains(diagnostics.String(), "identity_unavailable") {
		t.Fatalf("identity failure suppressed notice: %+v %s", r, &diagnostics)
	}
}
