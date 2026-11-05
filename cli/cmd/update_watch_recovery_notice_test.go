package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/filelock"
	"github.com/spf13/cobra"
)

func TestWatchRecoveryNoticePlanPrefetchDoesNotConsume(t *testing.T) {
	cfg, _ := runtimeTestConfig(t, "https://prefetch.example.invalid", true)
	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	for i := 0; i < 2; i++ {
		r := updateWatchRecovery{Status: "pending", Reason: "external_loop_unverified", Action: "verify the existing scheduler"}
		annotateWatchRecoveryNotice(cmd, cfg, &r)
		if r.Notification != "required" {
			t.Fatalf("plan %d consumed an unacknowledged notice: %+v", i+1, r)
		}
	}
}

func TestWatchRecoveryNoticeFeedFailureDoesNotConsume(t *testing.T) {
	feedFailed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/agent-context":
			_, _ = w.Write([]byte(`{"code":0,"data":{"context_revision":1,"control_context":{"context_revision":1,"intent_actions":[]}}}`))
		case "/api/v2/runtime/heartbeat":
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		case "/api/v2/feed":
			feedFailed = true
			http.Error(w, "Feed unavailable", http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cfg, server := runtimeTestConfig(t, srv.URL, true)
	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	first := updateWatchRecovery{Status: "pending", Reason: "external_loop_unverified", Action: "verify the existing scheduler"}
	annotateWatchRecoveryNotice(cmd, cfg, &first)
	if err := pollFeedV2(feedPollCmd, server, "20"); err == nil || !feedFailed {
		t.Fatalf("expected failed Feed request: %v, requested=%v", err, feedFailed)
	}
	second := updateWatchRecovery{Status: first.Status, Reason: first.Reason, Action: first.Action}
	annotateWatchRecoveryNotice(cmd, cfg, &second)
	if second.Notification != "required" || second.NotificationID != first.NotificationID || !validWatchRecoveryNoticeID(second.NotificationID) {
		t.Fatalf("Feed failure consumed or replaced notice: first=%+v second=%+v", first, second)
	}
}

func TestWatchRecoveryNoticeTransitions(t *testing.T) {
	cfg, _ := runtimeTestConfig(t, "https://notice.example.invalid", true)
	cmd := &cobra.Command{}
	var diagnostics bytes.Buffer
	cmd.SetErr(&diagnostics)
	base := updateWatchRecovery{Status: "pending", Reason: "external_loop_unverified", Action: "verify the existing scheduler"}
	check := func(r updateWatchRecovery, want string) string {
		t.Helper()
		before := r
		annotateWatchRecoveryNotice(cmd, cfg, &r)
		if r.Notification != want {
			t.Fatalf("notification=%q want=%q result=%+v", r.Notification, want, r)
		}
		id := r.NotificationID
		r.Notification, r.NotificationID = before.Notification, before.NotificationID
		if r != before {
			t.Fatalf("notice changed recovery result: before=%+v after=%+v", before, r)
		}
		return id
	}
	ack := func(id string) {
		t.Helper()
		if err := acknowledgeWatchRecoveryNotice(id); err != nil {
			t.Fatal(err)
		}
	}
	id := check(base, "required")
	if again := check(base, "required"); again != id {
		t.Fatal("prefetch replaced the pending notice")
	}
	ack(id)
	ack(id)
	check(base, "unchanged")
	changed := base
	changed.Status = "blocked"
	ack(check(changed, "required"))
	if err := acknowledgeWatchRecoveryNotice(id); err == nil {
		t.Fatal("old notice acknowledged a changed failure")
	}
	check(changed, "unchanged")
	changed.Reason = "host_managed_unverified"
	ack(check(changed, "required"))
	changed.Action = "verify the existing plugin"
	lastFailure := check(changed, "required")
	ack(lastFailure)
	check(changed, "unchanged")
	check(updateWatchRecovery{Status: "blocked", Reason: "identity_changed"}, "")
	check(updateWatchRecovery{Status: "skipped", Reason: "owner_disabled"}, "")
	check(changed, "unchanged")
	recovered := check(updateWatchRecovery{Status: "ready", RuntimeReady: true}, "recovered")
	if again := check(updateWatchRecovery{Status: "ready", RuntimeReady: true}, "recovered"); again != recovered {
		t.Fatal("ready prefetch replaced the pending recovery")
	}
	if err := acknowledgeWatchRecoveryNotice(id); err == nil {
		t.Fatal("old failure acknowledged recovery")
	}
	ack(recovered)
	check(updateWatchRecovery{Status: "ready", RuntimeReady: true}, "")
	regressed := check(changed, "required")
	if err := acknowledgeWatchRecoveryNotice(recovered); err == nil {
		t.Fatal("old recovery acknowledged a new failure")
	}
	if err := acknowledgeWatchRecoveryNotice(lastFailure); err == nil {
		t.Fatal("old failure acknowledged its later recurrence")
	}
	if regressed == id || regressed == recovered {
		t.Fatal("new event reused an old notice ID")
	}
	ack(regressed)
	check(changed, "unchanged")
	if diagnostics.Len() != 0 {
		t.Fatalf("normal transitions emitted diagnostics: %s", &diagnostics)
	}
}

func TestWatchRecoveryNoticeReadyWithoutPriorStateStaysQuiet(t *testing.T) {
	cfg, _ := runtimeTestConfig(t, "https://ready.example.invalid", true)
	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	path, _, err := watchRecoveryNoticeScope(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, corrupt := range []bool{false, true} {
		if corrupt {
			if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		r := updateWatchRecovery{Status: "ready", RuntimeReady: true}
		annotateWatchRecoveryNotice(cmd, cfg, &r)
		if r.Notification != "" || r.NotificationID != "" {
			t.Fatalf("missing prior state invented a recovery notification: %+v", r)
		}
	}
}

func TestWatchRecoveryNoticeWriteFailuresKeepRecoveryPending(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires user-level POSIX directory permissions")
	}
	cfg, _ := runtimeTestConfig(t, "https://write-failure.example.invalid", true)
	cmd := &cobra.Command{}
	var diagnostics bytes.Buffer
	cmd.SetErr(&diagnostics)
	failure := updateWatchRecovery{Status: "pending", Reason: "external_loop_unverified", Action: "verify scheduler"}
	annotateWatchRecoveryNotice(cmd, cfg, &failure)
	path, scope, err := watchRecoveryNoticeScope(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	if err := acknowledgeWatchRecoveryNotice(failure.NotificationID); err == nil {
		t.Fatal("acknowledgment claimed success without persisting")
	}
	recovered := updateWatchRecovery{Status: "ready", RuntimeReady: true}
	annotateWatchRecoveryNotice(cmd, cfg, &recovered)
	if recovered.Notification != "recovered" || recovered.NotificationID != "" || !strings.Contains(diagnostics.String(), "state_write_failed") {
		t.Fatalf("failed save consumed the recovery transition: %+v %s", recovered, &diagnostics)
	}
	state, err := readWatchRecoveryNotice(path, scope)
	if err != nil || !state.Active || state.Acknowledged || state.ID != failure.NotificationID {
		t.Fatalf("failed write changed the prior pending state: %+v %v", state, err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	annotateWatchRecoveryNotice(cmd, cfg, &recovered)
	if recovered.Notification != "recovered" || !validWatchRecoveryNoticeID(recovered.NotificationID) {
		t.Fatalf("recovery did not remain retryable: %+v", recovered)
	}
}

func TestWatchRecoveryNoticeIdentityIsolation(t *testing.T) {
	cfg, server := runtimeTestConfig(t, "https://first.example.invalid", true)
	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	base := updateWatchRecovery{Status: "pending", Reason: "external_loop_unverified", Action: "verify existing scheduler"}
	firstID := ""
	check := func(want string) {
		t.Helper()
		r := base
		annotateWatchRecoveryNotice(cmd, cfg, &r)
		if r.Notification != want {
			t.Fatalf("notification=%q want=%q result=%+v", r.Notification, want, r)
		}
		if want == "required" {
			if firstID == "" {
				firstID = r.NotificationID
			} else if err := acknowledgeWatchRecoveryNotice(firstID); err == nil {
				t.Fatal("another account or endpoint accepted the original notice ID")
			}
			if err := acknowledgeWatchRecoveryNotice(r.NotificationID); err != nil {
				t.Fatal(err)
			}
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

func TestWatchRecoveryNoticeLegacyStateRequiresAcknowledgment(t *testing.T) {
	for _, active := range []bool{true, false} {
		t.Run(map[bool]string{true: "failure", false: "recovery"}[active], func(t *testing.T) {
			cfg, _ := runtimeTestConfig(t, "https://legacy.example.invalid", true)
			cmd := &cobra.Command{}
			cmd.SetErr(&bytes.Buffer{})
			r := updateWatchRecovery{Status: "pending", Reason: "external_loop_unverified", Action: "verify scheduler"}
			annotateWatchRecoveryNotice(cmd, cfg, &r)
			if !active {
				r = updateWatchRecovery{Status: "ready"}
				annotateWatchRecoveryNotice(cmd, cfg, &r)
			}
			path, scope, err := watchRecoveryNoticeScope(cfg)
			if err != nil {
				t.Fatal(err)
			}
			state, err := readWatchRecoveryNotice(path, scope)
			if err != nil {
				t.Fatal(err)
			}
			state.ID, state.Acknowledged = "", true
			if err := saveMaintenance(path, state); err != nil {
				t.Fatal(err)
			}
			annotateWatchRecoveryNotice(cmd, cfg, &r)
			want := "required"
			if !active {
				want = "recovered"
			}
			if r.Notification != want || !validWatchRecoveryNoticeID(r.NotificationID) {
				t.Fatalf("legacy intent suppressed an unacknowledged notice: %+v", r)
			}
			if err := acknowledgeWatchRecoveryNotice(r.NotificationID); err != nil {
				t.Fatal(err)
			}
			annotateWatchRecoveryNotice(cmd, cfg, &r)
			if r.Notification == "required" || r.Notification == "recovered" {
				t.Fatalf("acknowledged migrated notice repeated: %+v", r)
			}
		})
	}
}

func TestWatchRecoveryNoticeAckCommandIsLocalWithObservationsDisabled(t *testing.T) {
	t.Setenv("EIGENFLUX_RUNTIME_OBSERVATIONS", "0")
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		t.Errorf("local acknowledgment made a network request: %s", r.URL.Path)
	}))
	defer srv.Close()
	cfg, _ := runtimeTestConfig(t, srv.URL, true)
	planCmd := &cobra.Command{}
	planCmd.SetErr(&bytes.Buffer{})
	r := updateWatchRecovery{Status: "pending", Reason: "external_loop_unverified", Action: "verify scheduler"}
	annotateWatchRecoveryNotice(planCmd, cfg, &r)
	ack := newHeartbeatRecoveryNoticeAckCmd()
	ack.SetOut(&bytes.Buffer{})
	for _, invalid := range []string{"", "not-an-id", strings.Repeat("a", 31), strings.Repeat("A", 32), strings.Repeat("0", 32)} {
		_ = ack.Flags().Set("notice", invalid)
		if err := ack.RunE(ack, nil); err == nil {
			t.Fatalf("invalid or unknown notice accepted: %q", invalid)
		}
	}
	_ = ack.Flags().Set("notice", r.NotificationID)
	for i := 0; i < 2; i++ {
		if err := ack.RunE(ack, nil); err != nil {
			t.Fatal(err)
		}
	}
	annotateWatchRecoveryNotice(planCmd, cfg, &r)
	if requests != 0 || r.Notification != "unchanged" {
		t.Fatalf("local acknowledgment depended on telemetry/network: requests=%d recovery=%+v", requests, r)
	}
}

func TestWatchRecoveryNoticeFailuresRemainActionableAndPrivate(t *testing.T) {
	cfg, server := runtimeTestConfig(t, "https://private-endpoint.example.invalid", true)
	cmd := &cobra.Command{}
	var diagnostics bytes.Buffer
	cmd.SetErr(&diagnostics)
	base := updateWatchRecovery{Status: "pending", Reason: "private-reason", Action: "private action /owner/private-path"}
	annotateWatchRecoveryNotice(cmd, cfg, &base)
	if base.Notification != "required" || !validWatchRecoveryNoticeID(base.NotificationID) {
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
	if r.Notification != "required" || !validWatchRecoveryNoticeID(r.NotificationID) || !strings.Contains(diagnostics.String(), "state_read_failed") {
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
		if r.Notification != "required" || r.NotificationID != "" || !strings.Contains(diagnostics.String(), "state_write_failed") {
			t.Fatalf("failed persistence suppressed notice: %+v %s", r, &diagnostics)
		}
		if strings.Contains(diagnostics.String(), config.HomeDir()) || strings.Contains(diagnostics.String(), "private") || diagnostics.Len() > 200 {
			t.Fatalf("unbounded or private diagnostic: %s", &diagnostics)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	held, acquired, err := filelock.TryAcquire(path + ".lock")
	if err != nil || !acquired {
		t.Fatalf("lock acquisition: %v %v", acquired, err)
	}
	for _, status := range []string{"pending", "ready"} {
		diagnostics.Reset()
		r = base
		want := "required"
		if status == "ready" {
			r.Status, r.Action, want = "ready", "", ""
		}
		annotateWatchRecoveryNotice(cmd, cfg, &r)
		if r.Notification != want || r.NotificationID != "" || !strings.Contains(diagnostics.String(), "state_locked") {
			t.Fatalf("lock failure consumed notice: %+v %s", r, &diagnostics)
		}
		if err := acknowledgeWatchRecoveryNotice(base.NotificationID); err == nil {
			t.Fatal("ack bypassed the held lock")
		}
	}
	held.Release()
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatal("lock path was not retained", err)
	}
	credentialsPath := filepath.Join(config.HomeDir(), "servers", server, "agent-v2-credentials.json")
	if err := os.Remove(credentialsPath); err != nil {
		t.Fatal(err)
	}
	diagnostics.Reset()
	r = base
	annotateWatchRecoveryNotice(cmd, cfg, &r)
	if r.Notification != "required" || r.NotificationID != "" || !strings.Contains(diagnostics.String(), "identity_unavailable") {
		t.Fatalf("identity failure suppressed notice: %+v %s", r, &diagnostics)
	}
}
