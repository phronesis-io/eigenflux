package cmd

import (
	"context"
	"net/http"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/config"
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
	ack(check("recovered"))
	check("")
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
	for _, min := range []string{"0.0.46", "0.0.45"} {
		r, _, err := updateHeartbeatCLI(&cobra.Command{}, cfg, min)
		if err != nil || r.Reason != "macos_host_watch" {
			t.Fatalf("handoff: %+v %v", r, err)
		}
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
}
