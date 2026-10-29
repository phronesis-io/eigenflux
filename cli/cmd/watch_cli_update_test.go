package cmd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/selfupdate"
)

func watchCLIUpdateFixture(t *testing.T) (*accountWatch, selfupdate.Options, *atomic.Int32) {
	t.Helper()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	m := selfupdate.Manifest{Version: "0.0.44"}
	if err := selfupdate.Sign(&m, key); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/cli/latest/release.json" {
			t.Errorf("unrelated request: %s", r.URL.Path)
		}
		_ = json.NewEncoder(out).Encode(m)
	}))
	t.Cleanup(srv.Close)
	w := newCommissionWatch(t, srv.URL)
	w.handoff = newWatchHandoff()
	path := filepath.Join(t.TempDir(), "eigenflux")
	if err := os.WriteFile(path, []byte("test installation"), 0700); err != nil {
		t.Fatal(err)
	}
	return w, selfupdate.Options{Executable: path, Version: version, Key: pub, CDN: srv.URL}, &calls
}

func TestWatchCLIUpdateHonorsExplicitServerSwitchAndSharedThrottle(t *testing.T) {
	w, options, calls := watchCLIUpdateFixture(t)
	cfg, _ := config.Load()
	if err := cfg.SetKV("auto_cli_update", "false"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.checkCLIUpdate(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("disabled automatic updates contacted CDN")
	}
	if err := cfg.SetServerKV(w.server.Name, "auto_cli_update", "true"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if done, err := w.checkCLIUpdate(context.Background(), options); err != nil || done {
			t.Fatalf("check failed: done=%v err=%v", done, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("daily throttle did not deduplicate checks: %d", calls.Load())
	}
	snapshot, err := maintenance.Snapshot(w.maintenanceScope())
	if err != nil || len(snapshot.Events) == 0 {
		t.Fatalf("missing existing maintenance observations: %v", err)
	}
	for _, event := range snapshot.Events {
		if event.Component != "cli" || event.Trigger != "auto" {
			t.Fatalf("incorrect observation: %+v", event)
		}
	}
	// The watch's server switch takes precedence over the installation default.
	if err := cfg.SetKV("auto_cli_update", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetServerKV(w.server.Name, "auto_cli_update", "false"); err != nil {
		t.Fatal(err)
	}
	options.CheckNow = true
	if _, err := w.checkCLIUpdate(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("server-specific opt-out was ignored")
	}
}

func TestWatchCLIUpdateRejectsAccountChangeAndClosedAdmission(t *testing.T) {
	for _, scenario := range []string{"identity", "draining"} {
		t.Run(scenario, func(t *testing.T) {
			w, options, calls := watchCLIUpdateFixture(t)
			if scenario == "draining" {
				w.handoff.request("1.0.0")
			} else {
				credentials := w.identity
				credentials.AgentID = "other-agent"
				if err := auth.SaveV2Credentials(w.server.Name, &credentials); err != nil {
					t.Fatal(err)
				}
			}
			done, err := w.checkCLIUpdate(context.Background(), options)
			if scenario == "identity" && !errors.Is(err, errWatchIdentity) {
				t.Fatalf("identity change accepted: %v", err)
			}
			if scenario == "draining" && (!done || err != nil) {
				t.Fatalf("draining loop did not stop: %v", err)
			}
			if calls.Load() != 0 {
				t.Fatal("unsafe watch contacted CDN")
			}
		})
	}
}

func TestWatchCLIUpdateFailureLeavesBusinessAdmissionOpen(t *testing.T) {
	w, options, calls := watchCLIUpdateFixture(t)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	options.Key = other
	if done, err := w.checkCLIUpdate(context.Background(), options); err != nil || done {
		t.Fatalf("update failure stopped watch: %v", err)
	}
	if calls.Load() != 1 || !w.handoff.begin() {
		t.Fatal("failed update closed business admission")
	}
	w.handoff.end()
	data, _ := os.ReadFile(options.Executable)
	if string(data) != "test installation" {
		t.Fatal("failed update changed installed executable")
	}
}

func TestNotificationOnlyWatchChecksCLIWithoutAgentBinding(t *testing.T) {
	_, options, calls := watchCLIUpdateFixture(t)
	w := newNotificationWatch(t, options.CDN)
	w.handoff = newWatchHandoff()
	if w.binding != nil || w.journal != nil || w.runAgent != nil {
		t.Fatal("notification-only watch gained an Agent execution lane")
	}
	if done, err := w.checkCLIUpdate(context.Background(), options); err != nil || done {
		t.Fatalf("notification-only update check failed: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("notification-only watch did not check the CDN")
	}
}

func TestWatchCLIUpdateExplainsFiveMinuteRetryWithoutRepeatedLogs(t *testing.T) {
	w, options, calls := watchCLIUpdateFixture(t)
	var events []map[string]interface{}
	w.emit = func(kind string, data interface{}) error {
		if kind == "cli_update_status" {
			events = append(events, data.(map[string]interface{}))
		}
		return nil
	}
	trusted := options.Key
	options.Key, _, _ = ed25519.GenerateKey(rand.Reader)
	options.Now = time.Now()
	if _, err := w.checkCLIUpdate(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	options.Key = trusted
	options.Now = options.Now.Add(4 * time.Minute)
	for range 2 {
		if _, err := w.checkCLIUpdate(context.Background(), options); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 || len(events) != 2 || events[1]["reason"] != "retry_interval" || events[1]["next_check_at"] == "" {
		t.Fatalf("retry decision is missing or repeated: calls=%d events=%+v", calls.Load(), events)
	}
	if _, exists := events[1]["error"]; exists {
		t.Fatal("raw updater error leaked into watch events")
	}
	options.Now = options.Now.Add(time.Minute)
	if _, err := w.checkCLIUpdate(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(events) != 3 || events[2]["status"] != "current" {
		t.Fatalf("update did not retry at five minutes: %+v", events)
	}
}
