package cmd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
)

func TestWorkBuddyMigrationMarkerAndRetry(t *testing.T) {
	var fail atomic.Bool
	var calls atomic.Int32
	fail.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			http.Error(out, "unavailable", 500)
			return
		}
		fmt.Fprint(out, `{"code":0,"data":{"orders":[],"next_cursor":""}}`)
	}))
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	w.binding.Host, w.binding.Mode = "workbuddy", "acp"
	settings, _ := config.Load()
	if err := settings.UpdateServerWithCommission(w.server.Name, w.server.Endpoint, w.server.StreamEndpoint, w.server.Endpoint); err != nil {
		t.Fatal(err)
	}
	w.server.CommissionEndpoint = w.server.Endpoint
	credentials, _ := auth.LoadV2Credentials(w.server.Name)
	credentials.ExpiresAt = time.Now().Add(time.Hour).UnixMilli()
	if err := auth.SaveV2Credentials(w.server.Name, credentials); err != nil {
		t.Fatal(err)
	}
	if err := dispatch.WriteJSON(dispatch.BindingPath(w.home, w.server.Name), w.binding); err != nil {
		t.Fatal(err)
	}
	if done, err := w.reconcileWorkBuddyMigration(context.Background()); err != nil || !done || calls.Load() != 0 {
		t.Fatalf("fresh binding scanned %v %v", done, err)
	}
	if err := dispatch.WriteJSON(notificationWatchPath(w.home, w.server.Name), w.binding); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(w.home, "watch", w.scope+".workbuddy-migration.json")
	if done, err := w.reconcileWorkBuddyMigration(context.Background()); err == nil || done {
		t.Fatal("failed scan marked complete")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("failure wrote completion marker")
	}
	fail.Store(false)
	if done, err := w.reconcileWorkBuddyMigration(context.Background()); err != nil || !done {
		t.Fatalf("retry %v %v", done, err)
	}
	previous := calls.Load()
	if done, err := w.reconcileWorkBuddyMigration(context.Background()); err != nil || !done || calls.Load() != previous {
		t.Fatalf("completed migration rescanned %v %v", done, err)
	}
	owner := *w.binding
	owner.AgentID = "other"
	if err := dispatch.WriteJSON(notificationWatchPath(w.home, w.server.Name), owner); err != nil {
		t.Fatal(err)
	}
	if done, err := w.reconcileWorkBuddyMigration(context.Background()); err != nil || !done || calls.Load() != previous {
		t.Fatalf("historical owner affected current watch %v %v", done, err)
	}
}

func TestWorkBuddyMigrationFailureKeepsWatchAlive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) { http.Error(out, "unavailable", 503) }))
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	w.binding.Host, w.binding.Mode = "workbuddy", "acp"
	settings, _ := config.Load()
	if err := settings.UpdateServerWithCommission(w.server.Name, w.server.Endpoint, w.server.StreamEndpoint, w.server.Endpoint); err != nil {
		t.Fatal(err)
	}
	w.server.CommissionEndpoint = w.server.Endpoint
	credentials, _ := auth.LoadV2Credentials(w.server.Name)
	credentials.ExpiresAt = time.Now().Add(time.Hour).UnixMilli()
	if err := auth.SaveV2Credentials(w.server.Name, credentials); err != nil {
		t.Fatal(err)
	}
	dispatch.WriteJSON(dispatch.BindingPath(w.home, w.server.Name), w.binding)
	dispatch.WriteJSON(notificationWatchPath(w.home, w.server.Name), w.binding)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pending := make(chan struct{}, 1)
	w.emit = func(kind string, _ interface{}) error {
		if kind == "commission_migration_status" {
			pending <- struct{}{}
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- w.workBuddyMigrationLoop(ctx) }()
	select {
	case <-pending:
	case err := <-done:
		t.Fatalf("migration terminated watch: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("missing pending state")
	}
	select {
	case err := <-done:
		t.Fatalf("stopped after recoverable error: %v", err)
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("did not cancel")
	}
}
