package cmd

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

type scriptedStreamDialer struct {
	statuses []int
	errors   []error
	headers  []http.Header
	urls     []string
}

func (d *scriptedStreamDialer) Dial(rawURL string, headers http.Header) (*websocket.Conn, *http.Response, error) {
	d.headers = append(d.headers, headers.Clone())
	d.urls = append(d.urls, rawURL)
	index := len(d.headers) - 1
	if index >= len(d.errors) {
		return nil, nil, nil
	}
	var response *http.Response
	if index < len(d.statuses) && d.statuses[index] != 0 {
		response = &http.Response{StatusCode: d.statuses[index], Body: io.NopCloser(strings.NewReader(""))}
	}
	return nil, response, d.errors[index]
}

func TestStreamHandshakeRefreshesV2CredentialsAndRetriesOnlyOnce(t *testing.T) {
	dialer := &scriptedStreamDialer{
		statuses: []int{http.StatusUnauthorized, 0},
		errors:   []error{errors.New("401 unauthorized"), nil},
	}
	refreshCalls := 0
	conn, credentials, attempted, err := dialStreamWithCredentialRefresh(dialer, "ws://example.test/api/v2/agent/events/ws?cursor=old-agent-cursor&keep=1",
		http.Header{"Authorization": []string{"Bearer stale"}}, "temporary-agent", func() (*auth.V2Credentials, error) {
			refreshCalls++
			return &auth.V2Credentials{AgentID: "historical-agent", AccessToken: "fresh"}, nil
		})
	if err != nil || conn != nil || !attempted {
		t.Fatalf("unexpected recovery result: conn=%v attempted=%v err=%v", conn, attempted, err)
	}
	if refreshCalls != 1 || len(dialer.headers) != 2 {
		t.Fatalf("refresh calls=%d dials=%d, want one refresh and two dials", refreshCalls, len(dialer.headers))
	}
	if credentials == nil || credentials.AgentID != "historical-agent" || credentials.AccessToken != "fresh" {
		t.Fatalf("authoritative identity was not returned: %+v", credentials)
	}
	if got := dialer.headers[1].Get("Authorization"); got != "Bearer fresh" {
		t.Fatalf("retry Authorization = %q, want refreshed token", got)
	}
	if strings.Contains(dialer.urls[1], "cursor=") || !strings.Contains(dialer.urls[1], "keep=1") {
		t.Fatalf("identity switch reused the old Agent cursor or lost unrelated query values: %q", dialer.urls[1])
	}
}

func TestStreamHandshakeStopsAfterRefreshedCredentialIsRejected(t *testing.T) {
	dialer := &scriptedStreamDialer{
		statuses: []int{http.StatusUnauthorized, http.StatusUnauthorized},
		errors:   []error{errors.New("first 401"), errors.New("second 401")},
	}
	refreshCalls := 0
	_, credentials, attempted, err := dialStreamWithCredentialRefresh(dialer, "ws://example.test/api/v2/agent/events/ws",
		http.Header{}, "temporary-agent", func() (*auth.V2Credentials, error) {
			refreshCalls++
			return &auth.V2Credentials{AgentID: "historical-agent", AccessToken: "fresh"}, nil
		})
	if !attempted || !errors.Is(err, errStreamUnauthorized) {
		t.Fatalf("second 401 was not returned as terminal auth failure: attempted=%v err=%v", attempted, err)
	}
	if refreshCalls != 1 || len(dialer.headers) != 2 {
		t.Fatalf("refresh calls=%d dials=%d, want a single retry", refreshCalls, len(dialer.headers))
	}
	if credentials == nil || credentials.AgentID != "historical-agent" {
		t.Fatalf("refreshed authoritative identity missing: %+v", credentials)
	}
}

func TestStreamHandshakeKeepsCursorWhenRefreshStaysOnTheSameAgent(t *testing.T) {
	dialer := &scriptedStreamDialer{
		statuses: []int{http.StatusUnauthorized, 0},
		errors:   []error{errors.New("401 unauthorized"), nil},
	}
	_, _, _, err := dialStreamWithCredentialRefresh(dialer,
		"ws://example.test/api/v2/agent/events/ws?cursor=same-agent-cursor",
		http.Header{}, "same-agent", func() (*auth.V2Credentials, error) {
			return &auth.V2Credentials{AgentID: "same-agent", AccessToken: "fresh"}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dialer.urls[1], "cursor=same-agent-cursor") {
		t.Fatalf("ordinary token rotation dropped a valid cursor: %q", dialer.urls[1])
	}
}

func TestStreamRefreshRestrictionWaitsAndCanRefreshAfterAccessChanges(t *testing.T) {
	for _, restriction := range []struct {
		status int
		code   string
	}{
		{http.StatusConflict, "ONBOARDING_REQUIRED"},
		{http.StatusForbidden, "AGENT_SCOPE_REQUIRED"},
	} {
		for _, once := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/once=%t", restriction.code, once), func(t *testing.T) {
				var dials, refreshes atomic.Int32
				upgrader := websocket.Upgrader{}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/v2/agents/me":
						fmt.Fprint(w, `{"code":0,"data":{"profile":{"agent_id":"agent-1"}}}`)
					case "/api/v2/agent-sessions/refresh-challenges":
						fmt.Fprint(w, `{"data":{"nonce":"fixture-nonce","issued_at":1}}`)
					case "/api/v2/agent-sessions/refresh":
						count := refreshes.Add(1)
						fmt.Fprintf(w, `{"data":{"agent_id":"agent-1","access_token":"fresh-%d","refresh_token":"refresh-%d","expires_at":%d}}`, count, count, time.Now().Add(time.Hour).UnixMilli())
					case "/api/v2/agent/events/ws":
						switch attempt := dials.Add(1); attempt {
						case 1, 3:
							w.WriteHeader(http.StatusUnauthorized)
							fmt.Fprint(w, `{"error":{"code":"AGENT_AUTH_INVALID","message":"rotate the fixture credential"}}`)
						case 2:
							if r.Header.Get("Authorization") != "Bearer fresh-1" {
								t.Error("restricted retry did not use the refreshed credential")
							}
							w.WriteHeader(restriction.status)
							fmt.Fprintf(w, `{"error":{"code":%q,"message":"baseline Feed remains available"}}`, restriction.code)
						case 4:
							if r.Header.Get("Authorization") != "Bearer fresh-2" {
								t.Error("access recovery did not rotate credentials again")
							}
							conn, err := upgrader.Upgrade(w, r, nil)
							if err != nil {
								t.Error(err)
								return
							}
							defer conn.Close()
							_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "fixture complete"), time.Now().Add(time.Second))
						default:
							t.Errorf("unexpected dial %d", attempt)
							w.WriteHeader(http.StatusUnauthorized)
						}
					default:
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer server.Close()
				cfg, serverName := runtimeTestConfig(t, server.URL, true)
				if err := cfg.UpdateServer(serverName, server.URL, "ws"+strings.TrimPrefix(server.URL, "http")); err != nil {
					t.Fatal(err)
				}
				oldDelay := streamAccessRetryDelay
				streamAccessRetryDelay = time.Millisecond
				t.Cleanup(func() { streamAccessRetryDelay = oldDelay })
				command := &cobra.Command{}
				command.Flags().String("cursor", "", "")
				command.Flags().Bool("once", once, "")
				err := streamCmd.RunE(command, nil)
				if once {
					var apiErr *client.APIError
					if !errors.As(err, &apiErr) || apiErr.StatusCode != restriction.status || apiErr.ErrorCode != restriction.code ||
						strings.Contains(err.Error(), "credential refresh failed") {
						t.Fatalf("one-shot restriction was misclassified: %v", err)
					}
					if dials.Load() != 2 || refreshes.Load() != 1 {
						t.Fatalf("one-shot retried after restriction: dials=%d refreshes=%d", dials.Load(), refreshes.Load())
					}
				} else if err != nil || dials.Load() != 4 || refreshes.Load() != 2 {
					t.Fatalf("stream did not recover after access changed: err=%v dials=%d refreshes=%d", err, dials.Load(), refreshes.Load())
				}
			})
		}
	}
}
