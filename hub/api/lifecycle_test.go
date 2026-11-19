package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"fluxspace.ai/hub/govern"
)

type recordingCloser struct{ closed bool }

func (c *recordingCloser) Close() error { c.closed = true; return nil }

type failingCloser struct{}

func (failingCloser) Close() error { return errors.New("close failed") }

// TestCloseOptional proves the small optional interface works whether or not a
// type carries Close(): with it, Close is invoked and its error propagated;
// without it (or for nil), the call is a safe no-op. This is exactly the branch
// that lets main shut down cleanly while the govern worktree may or may not have
// added Store.Close() yet.
func TestCloseOptional(t *testing.T) {
	withClose := &recordingCloser{}
	if err := closeOptional(withClose); err != nil {
		t.Fatalf("closer returned %v", err)
	}
	if !withClose.closed {
		t.Fatal("Close was not invoked on a value that implements closer")
	}

	if err := closeOptional(struct{ field int }{}); err != nil {
		t.Fatalf("value without Close returned %v, want nil", err)
	}
	if err := closeOptional(nil); err != nil {
		t.Fatalf("nil returned %v, want nil", err)
	}
	if err := closeOptional(failingCloser{}); err == nil {
		t.Fatal("closeOptional swallowed a real Close error")
	}
}

// TestCloseGovernanceStore covers the wiring from *govern.Components to the
// optional closer. The final assertion is conditional on the store actually
// exposing Close(), so the test stays valid whether or not that method exists.
func TestCloseGovernanceStore(t *testing.T) {
	if err := closeGovernanceStore(nil); err != nil {
		t.Fatalf("nil components = %v", err)
	}
	if err := closeGovernanceStore(&govern.Components{}); err != nil {
		t.Fatalf("components without a store = %v", err)
	}

	store, err := govern.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	components := &govern.Components{Store: store}
	if err := closeGovernanceStore(components); err != nil {
		t.Fatalf("close real store = %v", err)
	}
	// When the store implements closer, its append handle must now be released.
	if _, hasCloser := any(store).(closer); hasCloser {
		if err := store.AppendAudit(govern.AuditEntry{}); err == nil {
			t.Fatal("governance store was not closed by closeGovernanceStore")
		}
	}
}

// TestRunShutsDownAndReleasesListener cancels the context and asserts Run
// returns promptly, cleanly, and with the listener released.
func TestRunShutsDownAndReleasesListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: http.NewServeMux()}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, srv, ln, nil) }()

	// One request proves Serve is up and the listener is registered before we
	// cancel. The socket is already bound by net.Listen, so the connection is
	// queued and accepted as soon as Serve starts. DisableKeepAlives keeps the
	// connection from lingering as an idle conn during Shutdown.
	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	resp, err := client.Get("http://" + ln.Addr().String() + "/health")
	if err != nil {
		cancel()
		t.Fatalf("server never started serving: %v", err)
	}
	_ = resp.Body.Close()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned an error on clean shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return promptly after context cancellation")
	}

	// The listener must be released: Accept on a closed listener fails
	// immediately. Guard with a timeout so a regression (listener still open)
	// fails the test instead of hanging it.
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			_ = conn.Close()
		}
		acceptErr <- err
	}()
	select {
	case err := <-acceptErr:
		if err == nil {
			t.Fatal("listener still accepting after Run returned")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Accept blocked: listener was not released by Run")
	}
}

// TestRunSurfacesServeError proves real errors are not swallowed: with a
// pre-closed listener, Serve fails immediately and Run must return that error.
func TestRunSurfacesServeError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	srv := &http.Server{Handler: http.NewServeMux()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := Run(ctx, srv, ln, nil); err == nil {
		t.Fatal("Run swallowed a real Serve error")
	}
}
