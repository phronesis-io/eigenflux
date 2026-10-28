package cmd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/cache"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

func streamTestCommand(t *testing.T, cursor string, once bool) *cobra.Command {
	t.Helper()
	command := &cobra.Command{}
	command.Flags().String("cursor", cursor, "")
	command.Flags().Bool("once", once, "")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	command.SetContext(ctx)
	oldFormat := formatFlag
	formatFlag = "json"
	t.Cleanup(func() { formatFlag = oldFormat })
	return command
}

func configureStreamTestServer(t *testing.T, endpoint string, v2 bool) string {
	t.Helper()
	cfg, serverName := runtimeTestConfig(t, endpoint, v2)
	if err := cfg.UpdateServer(serverName, endpoint, "ws"+strings.TrimPrefix(endpoint, "http")); err != nil {
		t.Fatal(err)
	}
	cache.SaveProfile(serverName, &cache.Profile{AgentID: "agent-1"})
	if v2 {
		if _, _, _, err := auth.LoadOrCreateIdentity(serverName); err != nil {
			t.Fatal(err)
		}
	}
	return serverName
}

func TestStreamProactivelyRefreshesAcrossMultipleConnectionsAndKeepsCursor(t *testing.T) {
	var connections, refreshes atomic.Int32
	closedReaders := make(chan struct{}, 2)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/agent-sessions/refresh-challenges":
			fmt.Fprint(w, `{"data":{"nonce":"fixture-nonce","issued_at":1}}`)
		case "/api/v2/agent-sessions/refresh":
			count := refreshes.Add(1)
			fmt.Fprintf(w, `{"data":{"agent_id":"agent-1","access_token":"fresh-%d","refresh_token":"refresh-%d","expires_at":%d}}`, count, count, time.Now().Add(30500*time.Millisecond).UnixMilli())
		case "/api/v2/agent/events/ws":
			count := connections.Add(1)
			wantToken, wantCursor := "test-v2", "initial"
			if count > 1 {
				wantToken, wantCursor = fmt.Sprintf("fresh-%d", count-1), fmt.Sprintf("cursor-%d", count-1)
			}
			if r.Header.Get("Authorization") != "Bearer "+wantToken || r.URL.Query().Get("cursor") != wantCursor {
				t.Errorf("connection %d: token=%q cursor=%q; want token=%q cursor=%q", count, r.Header.Get("Authorization"), r.URL.Query().Get("cursor"), wantToken, wantCursor)
			}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			if count == 3 {
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "fixture complete"), time.Now().Add(time.Second))
				return
			}
			_ = conn.WriteJSON(map[string]interface{}{"type": "pm", "data": map[string]string{"next_cursor": fmt.Sprintf("cursor-%d", count)}})
			_, _, _ = conn.ReadMessage()
			closedReaders <- struct{}{}
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	serverName := configureStreamTestServer(t, server.URL, true)
	credentials, err := auth.LoadV2Credentials(serverName)
	if err != nil {
		t.Fatal(err)
	}
	credentials.ExpiresAt = time.Now().Add(30500 * time.Millisecond).UnixMilli()
	if err := auth.SaveV2Credentials(serverName, credentials); err != nil {
		t.Fatal(err)
	}
	command := streamTestCommand(t, "initial", false)
	if err := streamCmd.RunE(command, nil); err != nil {
		t.Fatal(err)
	}
	if command.Context().Err() != nil || connections.Load() != 3 || refreshes.Load() != 2 {
		t.Fatalf("proactive renewal incomplete: context=%v connections=%d refreshes=%d", command.Context().Err(), connections.Load(), refreshes.Load())
	}
	for range 2 {
		select {
		case <-closedReaders:
		case <-command.Context().Done():
			t.Fatal("a previous connection was left open")
		}
	}
}

func TestStreamCanRefreshAgainAfterSuccessfulHandshake(t *testing.T) {
	var dials, refreshes atomic.Int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/agent-sessions/refresh-challenges":
			fmt.Fprint(w, `{"data":{"nonce":"fixture-nonce","issued_at":1}}`)
		case "/api/v2/agent-sessions/refresh":
			count := refreshes.Add(1)
			fmt.Fprintf(w, `{"data":{"agent_id":"agent-1","access_token":"fresh-%d","refresh_token":"refresh-%d","expires_at":%d}}`, count, count, time.Now().Add(30500*time.Millisecond).UnixMilli())
		case "/api/v2/agent/events/ws":
			count := dials.Add(1)
			if count == 1 || count == 3 {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"error":{"code":"AGENT_AUTH_INVALID"}}`)
				return
			}
			wantToken := "fresh-1"
			if count == 4 {
				wantToken = "fresh-3"
			}
			if r.Header.Get("Authorization") != "Bearer "+wantToken {
				t.Errorf("dial %d token=%q; want %q", count, r.Header.Get("Authorization"), wantToken)
			}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			if count == 4 {
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "fixture complete"), time.Now().Add(time.Second))
				return
			}
			_ = conn.WriteJSON(map[string]interface{}{"type": "pm", "data": map[string]string{"next_cursor": "after-first-connection"}})
			_, _, _ = conn.ReadMessage()
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	configureStreamTestServer(t, server.URL, true)
	command := streamTestCommand(t, "", false)
	if err := streamCmd.RunE(command, nil); err != nil {
		t.Fatal(err)
	}
	if command.Context().Err() != nil || dials.Load() != 4 || refreshes.Load() != 3 {
		t.Fatalf("renewal budget was not reset: context=%v dials=%d refreshes=%d", command.Context().Err(), dials.Load(), refreshes.Load())
	}
}

func TestStreamOnceAndLegacyConnectionsDoNotRotateCredentials(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("v2=%t", v2), func(t *testing.T) {
			var connections atomic.Int32
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wantPath := "/ws/pm"
				if v2 {
					wantPath = "/api/v2/agent/events/ws"
				}
				if r.URL.Path != wantPath {
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if !v2 && (r.URL.Query().Get("token") != "test-v1" || r.Header.Get("Authorization") != "") {
					t.Error("legacy stream authentication changed")
				}
				connections.Add(1)
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				_ = conn.WriteJSON(map[string]interface{}{"type": "pm", "data": map[string]string{"next_cursor": "once-cursor"}})
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()
			configureStreamTestServer(t, server.URL, v2)
			command := streamTestCommand(t, "", true)
			if err := streamCmd.RunE(command, nil); err != nil {
				t.Fatal(err)
			}
			if command.Context().Err() != nil || connections.Load() != 1 {
				t.Fatalf("one-shot stream reconnected: context=%v connections=%d", command.Context().Err(), connections.Load())
			}
		})
	}
}

func TestStreamCredentialRefreshDelayIsBounded(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		expires time.Duration
		want    time.Duration
	}{
		{15 * time.Minute, 15*time.Minute - 30*time.Second},
		{30 * time.Second, time.Second},
		{-time.Minute, time.Second},
	} {
		got := streamCredentialRefreshDelay(&auth.V2Credentials{ExpiresAt: now.Add(tc.expires).UnixMilli()}, now)
		if delta := got - tc.want; delta < -time.Millisecond || delta > time.Millisecond {
			t.Errorf("expires=%s delay=%s; want %s", tc.expires, got, tc.want)
		}
	}
}

func TestStreamCancellationClosesActiveConnection(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("v2=%t", v2), func(t *testing.T) {
			closed := make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				cancel()
				_, _, _ = conn.ReadMessage()
				close(closed)
			}))
			defer server.Close()
			configureStreamTestServer(t, server.URL, v2)
			command := streamTestCommand(t, "", false)
			command.SetContext(ctx)
			if err := streamCmd.RunE(command, nil); err != nil {
				t.Fatal(err)
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("canceled stream left its connection open")
			}
		})
	}
}

func TestStreamReconnectReloadsChangedIdentityAndClearsCursor(t *testing.T) {
	var connections atomic.Int32
	var serverName string
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/agent/events/ws" {
			t.Errorf("unexpected authentication or request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		count := connections.Add(1)
		if count == 2 && (r.Header.Get("Authorization") != "Bearer changed-agent" || r.URL.Query().Get("cursor") != "") {
			t.Errorf("reconnect retained old identity: token=%q cursor=%q", r.Header.Get("Authorization"), r.URL.Query().Get("cursor"))
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if count == 2 {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "fixture complete"), time.Now().Add(time.Second))
			return
		}
		_ = conn.WriteJSON(map[string]interface{}{"type": "pm", "data": map[string]string{"next_cursor": "previous-agent-cursor"}})
		if err := auth.SaveV2Credentials(serverName, &auth.V2Credentials{AgentID: "agent-2", AccessToken: "changed-agent", RefreshToken: "changed-refresh", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}); err != nil {
			t.Error(err)
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	serverName = configureStreamTestServer(t, server.URL, true)
	credentials, err := auth.LoadV2Credentials(serverName)
	if err != nil {
		t.Fatal(err)
	}
	credentials.ExpiresAt = time.Now().Add(30500 * time.Millisecond).UnixMilli()
	if err := auth.SaveV2Credentials(serverName, credentials); err != nil {
		t.Fatal(err)
	}
	command := streamTestCommand(t, "initial", false)
	if err := streamCmd.RunE(command, nil); err != nil {
		t.Fatal(err)
	}
	if command.Context().Err() != nil || connections.Load() != 2 {
		t.Fatalf("changed identity was not adopted: context=%v connections=%d", command.Context().Err(), connections.Load())
	}
}

func TestStreamCancellationInterruptsHandshake(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
		close(closed)
	}))
	defer server.Close()
	configureStreamTestServer(t, server.URL, true)
	command := streamTestCommand(t, "", false)
	command.SetContext(ctx)
	started := time.Now()
	if err := streamCmd.RunE(command, nil); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("context cancellation waited for the handshake timeout")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("canceled stream left its handshake pending")
	}
}
