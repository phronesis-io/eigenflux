package cmd

import (
	"context"
	"net/http"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/profilestate"
	watchstate "cli.eigenflux.ai/internal/watch"
	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

func TestHostWatchTLSSelection(t *testing.T) {
	cfg, _ := runtimeTestConfig(t, "https://test.invalid", true)
	if err := cfg.SetKV(macOSHostMaintenanceKey, "true"); err != nil {
		t.Fatal(err)
	}
	transport, dialer := http.DefaultTransport, *websocket.DefaultDialer
	t.Cleanup(func() { http.DefaultTransport = transport; *websocket.DefaultDialer = dialer })
	for _, tc := range []struct {
		command *cobra.Command
		file    string
		custom  bool
	}{
		{watchCmd, "", false}, {watchCmd, "/etc/ssl/cert.pem", true}, {heartbeatCmd, "", true},
	} {
		http.DefaultTransport = transport
		*websocket.DefaultDialer = dialer
		t.Setenv("SSL_CERT_FILE", tc.file)
		configureMacOSTrust(tc.command)
		if (http.DefaultTransport != transport) != tc.custom || (websocket.DefaultDialer.TLSClientConfig != dialer.TLSClientConfig) != tc.custom {
			t.Fatalf("%s explicit=%q: incorrect HTTP/WSS roots", tc.command.CommandPath(), tc.file)
		}
	}
}

func TestHostWatchRecoveryNoticeLifecycle(t *testing.T) {
	cfg, _ := runtimeTestConfig(t, "https://test.invalid", true)
	cmd := &cobra.Command{}
	check := func(want string) string {
		t.Helper()
		r := macOSHostWatchStatus(cmd, cfg)
		if r.Notification != want {
			t.Fatalf("want %q: %+v", want, r)
		}
		return r.NotificationID
	}
	ack := func(id string) {
		t.Helper()
		if err := acknowledgeWatchRecoveryNotice(id); err != nil {
			t.Fatal(err)
		}
	}
	ack(check("required"))
	check("unchanged")
	b, err := watchBindingIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err = dispatch.WriteJSON(notificationWatchPath(b.Home, b.Server), b); err != nil {
		t.Fatal(err)
	}
	release, err := watchstate.Acquire(b.Home, b.Server)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	w := &accountWatch{home: b.Home, scope: b.Scope, notificationsOnly: true}
	w.server.Name = b.Server
	w.server.Endpoint = b.Endpoint
	live := newWatchLiveRecorder(w)
	live.event("notifications_ready", nil)
	if err = live.saveLocked(); err != nil {
		t.Fatal(err)
	}
	check("")
	live.event("desktop_notification_status", map[string]string{"status": "permission_or_setup_required", "code": "permission_denied", "action": "open_system_notification_settings"})
	if err = live.saveLocked(); err != nil {
		t.Fatal(err)
	}
	denied := macOSHostWatchStatus(cmd, cfg)
	if denied.Status != "blocked" || !denied.RuntimeReady || denied.Action != "open_system_notification_settings" || denied.Notification != "required" {
		t.Fatalf("denied: %+v", denied)
	}
	ack(denied.NotificationID)
	check("unchanged")
	live.event("desktop_notification_status", map[string]string{"status": "ready"})
	if err = live.saveLocked(); err != nil {
		t.Fatal(err)
	}
	ack(check("recovered"))
	check("")
	live.event("desktop_permission_status", desktopSetupStatus(desktopnotify.ErrPermission))
	if err = live.saveLocked(); err != nil {
		t.Fatal(err)
	}
	ack(check("required"))
	live.event("desktop_permission_status", desktopSetupStatus(nil))
	if err = live.saveLocked(); err != nil {
		t.Fatal(err)
	}
	ack(check("recovered"))
	live.event("desktop_notification_status", desktopSetupStatus(desktopnotify.ErrHelperMissing))
	live.event("desktop_permission_status", desktopSetupStatus(nil))
	if err = live.saveLocked(); err != nil {
		t.Fatal(err)
	}
	check("required")
}

func TestHostWatchMinimumWakesThrottledUpdate(t *testing.T) {
	w, options, calls := watchCLIUpdateFixture(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err = cfg.SetKV(macOSHostMaintenanceKey, "true"); err != nil {
		t.Fatal(err)
	}
	check := func(want int32) {
		t.Helper()
		if _, err := w.checkCLIUpdate(context.Background(), options); err != nil {
			t.Fatal(err)
		}
		if got := calls.Load(); got != want {
			t.Fatalf("CDN requests: got %d want %d", got, want)
		}
	}
	options.Now = time.Now()
	check(2)
	check(2)
	for _, change := range []struct {
		seq uint64
		min string
	}{{10, "0.0.47"}, {11, "0.0.46"}, {10, "0.0.47"}} {
		if err := recordHostMinimumCLI(w.server.Name, change.seq, change.min); err != nil {
			t.Fatal(err)
		}
	}
	if err := recordHostMinimumCLI(w.server.Name, 11, "0.0.99"); err == nil {
		t.Fatal("conflicting requirement accepted at the same sequence")
	}
	state, err := profilestate.LoadRuntime(w.home, w.server.Name)
	if err != nil || state.HostMinimumCLI != "0.0.46" {
		t.Fatalf("pending requirement %q: %v", state.HostMinimumCLI, err)
	}
	check(4)
	check(4)
	options.Now = options.Now.Add(6 * time.Minute)
	check(6)
	// A withdrawn requirement stops affecting host checks after its last lease.
	_, err = profilestate.UpdateRuntime(w.home, w.server.Name, func(s *profilestate.RuntimeState) (bool, error) {
		s.HostMinimumUntil = time.Now().Add(-time.Hour).Unix()
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	options.Now = options.Now.Add(6 * time.Minute)
	check(8)
	options.Now = options.Now.Add(6 * time.Minute)
	check(8)
	for _, change := range []struct {
		seq uint64
		min string
	}{{12, ""}, {11, "0.0.46"}} {
		if err := recordHostMinimumCLI(w.server.Name, change.seq, change.min); err != nil {
			t.Fatal(err)
		}
	}
	state, err = profilestate.LoadRuntime(w.home, w.server.Name)
	if err != nil || state.HostMinimumCLI != "" || state.HostSkillsSequence != 12 {
		t.Fatalf("cleared requirement: %+v %v", state, err)
	}
}
