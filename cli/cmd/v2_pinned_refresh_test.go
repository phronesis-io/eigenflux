package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
)

func pinnedFixture(t *testing.T, endpoint string) (string, *auth.V2Credentials) {
	t.Helper()
	_, server := runtimeTestConfig(t, endpoint, true)
	credentials, err := auth.LoadV2Credentials(server)
	if err != nil {
		t.Fatal(err)
	}
	credentials.ExpiresAt = time.Now().Add(-time.Minute).UnixMilli()
	if err := auth.SaveV2Credentials(server, credentials); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := auth.LoadOrCreateIdentity(server); err != nil {
		t.Fatal(err)
	}
	return server, credentials
}
func awaitPinnedError(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("pinned refresh did not stop")
		return nil
	}
}
func TestPinnedRefreshCancelsExpiredHTTPAndReleasesLock(t *testing.T) {
	for _, phase := range []string{"challenge", "refresh-body"} {
		t.Run(phase, func(t *testing.T) {
			entered := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if phase == "refresh-body" && r.URL.Path == "/api/v2/agent-sessions/refresh-challenges" {
					_, _ = io.WriteString(w, `{"data":{"nonce":"nonce","issued_at":123}}`)
					return
				}
				if phase == "refresh-body" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
				}
				close(entered)
				<-r.Context().Done()
			}))
			defer srv.Close()
			server, before := pinnedFixture(t, srv.URL)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := refreshPinnedV2Credentials(ctx, server, srv.URL, false, before.AgentID, before.PrincipalID)
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("refresh did not reach HTTP")
			}
			cancel()
			if err := awaitPinnedError(t, done); !errors.Is(err, context.Canceled) {
				t.Fatalf("want cancellation, got %v", err)
			}
			current, err := auth.LoadV2Credentials(server)
			if err != nil {
				t.Fatal(err)
			}
			if current.AccessToken != before.AccessToken || current.RefreshToken != before.RefreshToken || current.AgentID != before.AgentID {
				t.Fatal("cancelled refresh changed binding")
			}
			if err := auth.WithV2CredentialsLock(server, 100*time.Millisecond, func() error { return nil }); err != nil {
				t.Fatalf("lock leaked: %v", err)
			}
		})
	}
}
func TestPinnedRefreshCancelsWaitingForAnotherProcessLock(t *testing.T) {
	server, before := pinnedFixture(t, "http://127.0.0.1:1")
	locked, release := make(chan struct{}), make(chan struct{})
	ownerDone := make(chan error, 1)
	go func() {
		ownerDone <- auth.WithV2CredentialsLock(server, time.Second, func() error { close(locked); <-release; return nil })
	}()
	<-locked
	defer func() {
		close(release)
		if err := awaitPinnedError(t, ownerDone); err != nil {
			t.Fatal(err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := refreshPinnedV2Credentials(ctx, server, "http://127.0.0.1:1", true, before.AgentID, before.PrincipalID)
		done <- err
	}()
	if err := awaitPinnedError(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want cancellation, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(config.HomeDir(), "servers", server, ".agent-v2-refresh.lock")); err != nil {
		t.Fatal("waiter removed the owner's lock")
	}
}
func TestPinnedRefreshChecksBindingInsideLock(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer srv.Close()
	server, before := pinnedFixture(t, srv.URL)
	locked, release := make(chan struct{}), make(chan struct{})
	ownerDone := make(chan error, 1)
	go func() {
		ownerDone <- auth.WithV2CredentialsLock(server, time.Second, func() error { close(locked); <-release; return nil })
	}()
	<-locked
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := refreshPinnedV2Credentials(ctx, server, srv.URL, true, before.AgentID, before.PrincipalID)
		done <- err
	}()
	switched := *before
	switched.AgentID = "new-account"
	if err := auth.SaveV2Credentials(server, &switched); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := awaitPinnedError(t, ownerDone); err != nil {
		t.Fatal(err)
	}
	if err := awaitPinnedError(t, done); !errors.Is(err, errWatchIdentity) {
		t.Fatalf("want identity error, got %v", err)
	}
	if requests.Load() != 0 {
		t.Fatal("refresh contacted server after identity switched under lock")
	}
}
func TestPinnedRefreshRejectsChangedRemoteIdentityBeforeSave(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/agent-sessions/refresh-challenges" {
			_, _ = io.WriteString(w, `{"data":{"nonce":"nonce","issued_at":123}}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{"agent_id": "other", "principal_id": "other", "access_token": "new", "refresh_token": "new-refresh", "expires_at": time.Now().Add(time.Hour).UnixMilli()}})
	}))
	defer srv.Close()
	server, before := pinnedFixture(t, srv.URL)
	_, err := refreshPinnedV2Credentials(context.Background(), server, srv.URL, false, before.AgentID, before.PrincipalID)
	if !errors.Is(err, errWatchIdentity) {
		t.Fatalf("want identity error, got %v", err)
	}
	current, err := auth.LoadV2Credentials(server)
	if err != nil {
		t.Fatal(err)
	}
	if current.AgentID != before.AgentID || current.AccessToken != before.AccessToken {
		t.Fatal("pinned refresh persisted another account")
	}
}
func TestPinnedTransportPreservesClientDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	c := &http.Client{Timeout: 30 * time.Millisecond, Transport: pinnedRefreshTransport{parent: context.Background(), base: http.DefaultTransport}}
	_, err := c.Get(srv.URL)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request deadline overwritten: %v", err)
	}
}

func TestPinnedRefreshNeverCreatesMissingIdentity(t *testing.T) {
	server, before := pinnedFixture(t, "http://127.0.0.1:1")
	identity := filepath.Join(config.HomeDir(), "servers", server, "identity.json")
	if err := os.Remove(identity); err != nil {
		t.Fatal(err)
	}
	if _, err := refreshPinnedV2Credentials(context.Background(), server, "http://127.0.0.1:1", true, before.AgentID, before.PrincipalID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want missing identity, got %v", err)
	}
	if _, err := os.Stat(identity); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("background refresh created another private identity")
	}
}
