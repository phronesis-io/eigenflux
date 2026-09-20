package cmd

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"cli.eigenflux.ai/internal/client"
)

func TestRuntimeAccessUsesOnlyExplicitServerEvidence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		v2        bool
		status    int
		body      string
		want      runtimeAccess
		wantError bool
	}{
		{"baseline conflict", true, 409, `{"error":{"code":"ONBOARDING_REQUIRED","details":{"onboarding_state":"in_progress"}}}`, runtimeAccess{"baseline", "in_progress"}, false},
		{"baseline forbidden", true, 403, `{"error":{"code":"ONBOARDING_INCOMPLETE","details":{"onboarding_state":"in_progress"}}}`, runtimeAccess{"baseline", "in_progress"}, false},
		{"baseline without details", true, 409, `{"error":{"code":"ONBOARDING_REQUIRED"}}`, runtimeAccess{"baseline", "in_progress"}, false},
		{"completed", true, 200, `{"code":0,"data":{"context_revision":1}}`, runtimeAccess{"intent_aligned", "completed"}, false},
		{"legacy completed", false, 200, `{"code":0,"data":{"profile":{"agent_id":"agent-1"}}}`, runtimeAccess{"legacy", "completed"}, false},
		{"revoked credential", true, 401, `{"error":{"code":"AGENT_CREDENTIAL_REVOKED","message":"revoked"}}`, runtimeAccess{}, true},
		{"other forbidden", true, 403, `{"error":{"code":"SCOPE_REQUIRED"}}`, runtimeAccess{}, true},
		{"other conflict", true, 409, `{"error":{"code":"CONFLICT"}}`, runtimeAccess{}, true},
		{"server failure", true, 503, `{"error":{"code":"UNAVAILABLE"}}`, runtimeAccess{}, true},
		{"malformed json", true, 200, `not-json`, runtimeAccess{}, true},
		{"missing context", true, 200, `{"code":0,"data":{}}`, runtimeAccess{}, true},
		{"null context", true, 200, `{"code":0,"data":null}`, runtimeAccess{}, true},
		{"invalid revision", true, 200, `{"code":0,"data":{"context_revision":"1"}}`, runtimeAccess{}, true},
		{"business failure", true, 200, `{"code":1,"data":{"context_revision":1}}`, runtimeAccess{}, true},
		{"legacy business failure", false, 200, `{"code":1,"msg":"unavailable"}`, runtimeAccess{}, true},
		{"legacy missing profile", false, 200, `{"code":0,"data":{}}`, runtimeAccess{}, true},
		{"contradictory onboarding details", true, 409, `{"error":{"code":"ONBOARDING_REQUIRED","details":{"onboarding_state":"completed"}}}`, runtimeAccess{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wantPath := "/agent-context"
				if !tc.v2 {
					wantPath = "/agents/me"
				}
				if r.Method != http.MethodGet || r.URL.Path != wantPath {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			got, err := readRuntimeAccess(client.New(server.URL, "fixture", "test", client.Meta{}), tc.v2)
			if tc.wantError {
				if err == nil || got != (runtimeAccess{}) {
					t.Fatalf("failure became usable access: %+v, %v", got, err)
				}
				if tc.status >= 400 {
					var apiErr *client.APIError
					if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status {
						t.Fatalf("server diagnostic lost: %v", err)
					}
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("access = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestRuntimeAccessNetworkFailureDoesNotBecomeBaseline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	for _, v2 := range []bool{true, false} {
		got, err := readRuntimeAccess(client.New(server.URL, "fixture", "test", client.Meta{}), v2)
		if err == nil || got != (runtimeAccess{}) {
			t.Fatalf("network failure became access: v2=%t access=%+v err=%v", v2, got, err)
		}
	}
}
