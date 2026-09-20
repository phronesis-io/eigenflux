package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"github.com/gorilla/websocket"
)

func TestStreamRestrictionsRetainTypedErrorsWithoutCredentialRefresh(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{
		{409, "ONBOARDING_REQUIRED"}, {403, "AGENT_SCOPE_REQUIRED"}, {503, "AGENT_AUTH_UNAVAILABLE"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, `{"error":{"code":%q,"message":"central restriction; baseline Feed remains available"}}`, tc.code)
			}))
			defer server.Close()
			refreshes := 0
			_, _, attempted, err := dialStreamWithCredentialRefresh(websocket.DefaultDialer,
				"ws"+strings.TrimPrefix(server.URL, "http"), http.Header{}, "agent-1", func() (*auth.V2Credentials, error) {
					refreshes++
					return nil, errors.New("must not refresh")
				})
			var apiErr *client.APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status || apiErr.ErrorCode != tc.code ||
				!strings.Contains(err.Error(), "baseline Feed remains available") || attempted || refreshes != 0 {
				t.Fatalf("err=%v attempted=%v refreshes=%d", err, attempted, refreshes)
			}
		})
	}
}

func TestQueuedEventsUseV2CredentialsAndKeepRestrictionErrors(t *testing.T) {
	status := http.StatusConflict
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v2/items/events" || r.Header.Get("Authorization") != "Bearer test-v2" {
			t.Errorf("wrong event request: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			fmt.Fprint(w, `{"code":0,"data":{}}`)
			return
		}
		fmt.Fprint(w, `{"error":{"code":"ONBOARDING_REQUIRED","message":"read-only baseline Feed remains available"}}`)
	}))
	defer server.Close()
	runtimeTestConfig(t, server.URL, true)
	events := []map[string]interface{}{{"item_id": "42", "kind": "surface"}}
	var apiErr *client.APIError
	if err := pushEvents(events); !errors.As(err, &apiErr) || apiErr.ErrorCode != "ONBOARDING_REQUIRED" {
		t.Fatalf("restricted V2 upload: %v", err)
	}
	status = http.StatusOK
	if err := pushEvents(events); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests=%d", requests)
	}
}
