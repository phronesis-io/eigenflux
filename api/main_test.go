package main

import (
	"context"
	"eigenflux_server/api/consolev2"
	"errors"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"eigenflux_server/pkg/config"
)

type mainTestListener struct{ net.Listener }

type managedServerFake struct {
	run           chan error
	mu            sync.Mutex
	shutdownCalls int
	closeCalls    int
}

func newManagedServerFake() *managedServerFake { return &managedServerFake{run: make(chan error, 1)} }
func (s *managedServerFake) Run() error        { return <-s.run }
func (s *managedServerFake) Shutdown(context.Context) error {
	s.mu.Lock()
	s.shutdownCalls++
	s.mu.Unlock()
	select {
	case s.run <- nil:
	default:
	}
	return nil
}
func (s *managedServerFake) Close() error {
	s.mu.Lock()
	s.closeCalls++
	s.mu.Unlock()
	return nil
}

func enabledMainIntegrationMode(t *testing.T) config.CommissionIntegration {
	t.Helper()
	mode, err := (&config.Config{
		AppEnv: "test", EnableCommissionIndex: true,
		CommissionIntegrationFlag: "true", IntegrationControlAddr: "127.0.0.1:18081",
		IntegrationControlToken: "0123456789abcdef0123456789abcdef",
	}).CommissionIntegrationMode()
	if err != nil {
		t.Fatal(err)
	}
	return mode
}

func TestReserveIntegrationListenerPrebindsOnlyEnabledMode(t *testing.T) {
	called := false
	listener := &mainTestListener{}
	got, err := reserveIntegrationListener(config.CommissionIntegration{}, func(string, string) (net.Listener, error) {
		called = true
		return listener, nil
	})
	if err != nil || got != nil || called {
		t.Fatalf("disabled reserve listener=%v called=%v error=%v", got, called, err)
	}

	mode := enabledMainIntegrationMode(t)
	got, err = reserveIntegrationListener(mode, func(network, address string) (net.Listener, error) {
		called = true
		if network != "tcp" || address != mode.ControlAddr {
			t.Fatalf("listen=%q %q", network, address)
		}
		return listener, nil
	})
	if err != nil || got != listener || !called {
		t.Fatalf("enabled reserve listener=%v called=%v error=%v", got, called, err)
	}
}

func TestReserveIntegrationListenerFailsClosedOnBindError(t *testing.T) {
	want := errors.New("address occupied")
	listener, err := reserveIntegrationListener(enabledMainIntegrationMode(t), func(string, string) (net.Listener, error) {
		return nil, want
	})
	if listener != nil || !errors.Is(err, want) {
		t.Fatalf("listener=%v error=%v", listener, err)
	}
}

func TestRunSupervisedStopsBothServersOnFailure(t *testing.T) {
	public := newManagedServerFake()
	private := newManagedServerFake()
	public.run <- errors.New("public failed")
	err := runSupervised(context.Background(), public, private)
	if err == nil {
		t.Fatal("server failure was ignored")
	}
	public.mu.Lock()
	publicShutdown := public.shutdownCalls
	public.mu.Unlock()
	private.mu.Lock()
	privateShutdown := private.shutdownCalls
	private.mu.Unlock()
	if publicShutdown != 1 || privateShutdown != 1 {
		t.Fatalf("shutdown calls public=%d private=%d", publicShutdown, privateShutdown)
	}
}

func TestRunSupervisedStopsBothServersOnContextCancellation(t *testing.T) {
	public := newManagedServerFake()
	private := newManagedServerFake()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- runSupervised(ctx, public, private) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not stop")
	}
}

func TestWalletKYCRoutesRequireConsoleAuthentication(t *testing.T) {
	h := server.New()
	registerConsoleV2BusinessBFF(h, &consolev2.Service{}, &config.Config{})
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/api/v2/console/bff/payout-method/kyc", http.StatusUnauthorized},
		{http.MethodPost, "/api/v2/console/bff/payout-method/kyc", http.StatusForbidden},
		{http.MethodPost, "/api/v2/console/bff/payout-method/kyc/authorization", http.StatusForbidden},
	} {
		response := ut.PerformRequest(h.Engine, tc.method, tc.path, nil).Result()
		if response.StatusCode() != tc.status {
			t.Fatalf("%s %s: got %d want %d", tc.method, tc.path, response.StatusCode(), tc.status)
		}
	}
}
