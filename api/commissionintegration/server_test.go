package commissionintegration

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"

	"eigenflux_server/pkg/config"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol"
)

const (
	integrationToken = "0123456789abcdef0123456789abcdef"
	integrationRunID = "run-0001"
)

func testIntegrationMode(t *testing.T, address string) config.CommissionIntegration {
	t.Helper()
	mode, err := (&config.Config{
		AppEnv: "test", EnableCommissionIndex: true,
		CommissionIntegrationFlag: "true", IntegrationControlAddr: address,
		IntegrationControlToken: integrationToken,
	}).CommissionIntegrationMode()
	if err != nil {
		t.Fatal(err)
	}
	return mode
}

func newTestIntegrationServer(t *testing.T, service *Service) *server.Hertz {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	control, err := NewServer(testIntegrationMode(t, listener.Addr().String()), service, listener)
	if err != nil {
		t.Fatal(err)
	}
	return control
}

func integrationHeaders() []ut.Header {
	return []ut.Header{
		{Key: "Authorization", Value: "Bearer " + integrationToken},
		{Key: "X-Integration-Run-ID", Value: integrationRunID},
	}
}

func performIntegrationRequest(h *server.Hertz, method, path, body string, headers ...ut.Header) *protocol.Response {
	var requestBody *ut.Body
	if body != "" {
		requestBody = &ut.Body{Body: bytes.NewBufferString(body), Len: len(body)}
	}
	return ut.PerformRequest(h.Engine, method, path, requestBody, headers...).Result()
}

func TestNewServerRequiresEnabledModeServiceAndExactPreboundListener(t *testing.T) {
	if h, err := NewServer(config.CommissionIntegration{}, readyService(t), nil); h != nil || err != config.ErrCommissionIntegrationDisabled {
		t.Fatalf("disabled NewServer() server=%v error=%v", h, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	mode := testIntegrationMode(t, listener.Addr().String())
	if h, err := NewServer(mode, nil, listener); h != nil || err != ErrInvalidConfiguration {
		t.Fatalf("missing service server=%v error=%v", h, err)
	}
	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	if h, err := NewServer(mode, readyService(t), other); h != nil || err != ErrInvalidConfiguration {
		t.Fatalf("wrong listener server=%v error=%v", h, err)
	}
}

func TestPrivateRoutesRequireControlTokenAndValidRunID(t *testing.T) {
	h := newTestIntegrationServer(t, readyService(t))
	for name, headers := range map[string][]ut.Header{
		"missing":      nil,
		"wrong bearer": {{Key: "Authorization", Value: "Bearer wrong"}, {Key: "X-Integration-Run-ID", Value: integrationRunID}},
		"agent bearer": {{Key: "Authorization", Value: "Bearer at_0123456789abcdef0123456789abcdef"}, {Key: "X-Integration-Run-ID", Value: integrationRunID}},
		"invalid run":  {{Key: "Authorization", Value: "Bearer " + integrationToken}, {Key: "X-Integration-Run-ID", Value: "INVALID"}},
	} {
		t.Run(name, func(t *testing.T) {
			response := performIntegrationRequest(h, http.MethodGet, statusPath, "", headers...)
			if response.StatusCode() != http.StatusUnauthorized || string(response.Body()) != `{"code":401,"msg":"unauthorized"}` {
				t.Fatalf("response=%d %s", response.StatusCode(), response.Body())
			}
			if strings.Contains(string(response.Body()), integrationToken) || strings.Contains(string(response.Body()), "at_") {
				t.Fatalf("response leaked credential: %s", response.Body())
			}
		})
	}
}

func TestStatusRouteReturnsExactHandshake(t *testing.T) {
	h := newTestIntegrationServer(t, readyService(t))
	response := performIntegrationRequest(h, http.MethodGet, statusPath, "", integrationHeaders()...)
	want := `{"mode":"test","consumer_ready":true,"source_ready":true,"es_ready":true,"embedding_ready":true,"embedding_provider":"ollama","embedding_dimensions":768,"es_index_dimensions":768,"capabilities":["commission-projection-diagnostics"]}`
	if response.StatusCode() != http.StatusOK || string(response.Body()) != want {
		t.Fatalf("response=%d %s", response.StatusCode(), response.Body())
	}
}

func TestDiagnosticRoutePreservesInt64IDAndMetadataOnly(t *testing.T) {
	h := newTestIntegrationServer(t, readyService(t))
	response := performIntegrationRequest(h, http.MethodGet, "/internal/integration/v1/commissions/9223372036854775000/diagnostics", "", integrationHeaders()...)
	if response.StatusCode() != http.StatusOK {
		t.Fatalf("response=%d %s", response.StatusCode(), response.Body())
	}
	body := string(response.Body())
	for _, expected := range []string{`"commission_id":"9223372036854775000"`, `"stream_last_id":"123-0"`, `"catalogue_version":11`, `"es_catalogue_version":10`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %s in %s", expected, body)
		}
	}
	for _, secret := range []string{"private", "vector", "payload", "embedding", integrationToken} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(secret)) {
			t.Fatalf("leaked %q in %s", secret, body)
		}
	}
}

func TestPrivateServerRejectsBodiesIDsMethodsAndPathsGenerically(t *testing.T) {
	h := newTestIntegrationServer(t, readyService(t))
	tests := []struct {
		name, method, path, body string
		status                   int
	}{
		{"status body", http.MethodGet, statusPath, `{}`, http.StatusBadRequest},
		{"diagnostic body", http.MethodGet, "/internal/integration/v1/commissions/42/diagnostics", `{}`, http.StatusBadRequest},
		{"zero ID", http.MethodGet, "/internal/integration/v1/commissions/0/diagnostics", "", http.StatusBadRequest},
		{"negative ID", http.MethodGet, "/internal/integration/v1/commissions/-1/diagnostics", "", http.StatusBadRequest},
		{"overflow ID", http.MethodGet, "/internal/integration/v1/commissions/9223372036854775808/diagnostics", "", http.StatusBadRequest},
		{"nonnumeric ID", http.MethodGet, "/internal/integration/v1/commissions/private-source/diagnostics", "", http.StatusBadRequest},
		{"wrong method", http.MethodPost, statusPath, "", http.StatusMethodNotAllowed},
		{"wrong path", http.MethodGet, "/internal/integration/v1/private-source", "", http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			response := performIntegrationRequest(h, tc.method, tc.path, tc.body, integrationHeaders()...)
			if response.StatusCode() != tc.status || len(response.Body()) > 96 {
				t.Fatalf("response=%d %s", response.StatusCode(), response.Body())
			}
			if strings.Contains(string(response.Body()), tc.path) || tc.body != "" && strings.Contains(string(response.Body()), tc.body) {
				t.Fatalf("response echoed input: %s", response.Body())
			}
		})
	}
}

func TestDiagnosticDependencyFailureReturnsBoundedPartialEvidence(t *testing.T) {
	service, err := NewService(projectionFake{err: context.DeadlineExceeded}, sourceFake{}, indexFake{}, embeddingFake{})
	if err != nil {
		t.Fatal(err)
	}
	h := newTestIntegrationServer(t, service)
	response := performIntegrationRequest(h, http.MethodGet, "/internal/integration/v1/commissions/42/diagnostics", "", integrationHeaders()...)
	body := string(response.Body())
	if response.StatusCode() != http.StatusOK || len(body) > 1024 || !strings.Contains(body, `"projection_status":"unavailable"`) || !strings.Contains(body, `"es_status":"ok"`) {
		t.Fatalf("response=%d %s", response.StatusCode(), response.Body())
	}
	if strings.Contains(body, context.DeadlineExceeded.Error()) {
		t.Fatalf("response leaked dependency error: %s", body)
	}
}

func TestInternalRoutesAreAbsentFromSeparatePublicRouter(t *testing.T) {
	public := server.Default()
	for _, path := range []string{statusPath, "/internal/integration/v1/commissions/42/diagnostics"} {
		response := performIntegrationRequest(public, http.MethodGet, path, "", integrationHeaders()...)
		if response.StatusCode() != http.StatusNotFound {
			t.Fatalf("public route %q returned %d", path, response.StatusCode())
		}
	}
}
