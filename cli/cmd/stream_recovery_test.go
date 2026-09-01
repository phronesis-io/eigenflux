package cmd

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/auth"

	"github.com/gorilla/websocket"
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
