package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/controlcontext"
	watchstate "cli.eigenflux.ai/internal/watch"
	"github.com/gorilla/websocket"
)

func TestWatchTransportsReadyAndCancellation(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-v2" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/v2/agent/events/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			_ = conn.WriteJSON(map[string]interface{}{"type": "pm_push", "data": map[string]interface{}{"messages": []interface{}{}, "next_cursor": "123"}})
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		case "/api/v2/runtime/heartbeat":
			var request map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["applied_context_revision"] != float64(7) {
				t.Errorf("lost applied context: %v (%v)", request, err)
			}
			_, _ = io.WriteString(w, `{"code":0,"data":{"pending_command_ids":["8"],"next_heartbeat_seconds":30}}`)
		case "/api/v2/runtime/control/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: command_available\ndata: {\"command_id\":\"8\"}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	cfg, name := runtimeTestConfig(t, srv.URL, true)
	if err := cfg.UpdateServer(name, srv.URL, strings.Replace(srv.URL, "http:", "ws:", 1)); err != nil {
		t.Fatal(err)
	}
	server, _ := cfg.GetActive(name)
	if err := controlcontext.Save(name, controlcontext.Snapshot{OwnerAgentID: "agent-1", Revision: 7, Context: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	identity, _ := auth.LoadV2Credentials(name)
	home, err := watchstate.CanonicalHome(config.HomeDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &accountWatch{home: home, server: *server, identity: *identity, scope: watchstate.Scope(home, name, identity.AgentID, identity.PrincipalID), wake: make(chan struct{}, 1)}
	reader, writer := io.Pipe()
	defer reader.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.run(ctx, writer); writer.Close() }()
	events := make(chan watchEvent, 20)
	go func() {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			var e watchEvent
			if json.Unmarshal(scanner.Bytes(), &e) == nil {
				events <- e
			}
		}
		close(events)
	}()
	seen := map[string]bool{}
	for !seen["pm_push"] || !seen["runtime_ready"] || !seen["control_pending"] {
		select {
		case e := <-events:
			if len(seen) == 0 && e.Type != "started" {
				t.Fatal("started must precede events")
			}
			if e.SchemaVersion != "eigenflux_watch.v1" || e.StateScope != w.scope || e.EventID == "" {
				t.Fatalf("invalid envelope: %+v", e)
			}
			seen[e.Type] = true
		case <-ctx.Done():
			t.Fatalf("missing events: %v", seen)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not close long connections")
	}
}

func TestWatchRejectsIdentitySwitchBeforeRefresh(t *testing.T) {
	_, name := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	before, _ := auth.LoadV2Credentials(name)
	w := &accountWatch{identity: *before}
	w.server.Name = name
	after := *before
	after.AgentID = "other-agent"
	if err := auth.SaveV2Credentials(name, &after); err != nil {
		t.Fatal(err)
	}
	if _, err := w.credentials(context.Background(), true); err != errWatchIdentity {
		t.Fatalf("want pinned identity error, got %v", err)
	}
}

func TestWatchFlushesTerminalIdentityDiagnostic(t *testing.T) {
	cfg, name := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	server, _ := cfg.GetActive(name)
	before, _ := auth.LoadV2Credentials(name)
	w := &accountWatch{home: config.HomeDir(), server: *server, identity: *before, scope: "pinned", wake: make(chan struct{}, 1)}
	after := *before
	after.AgentID = "other-agent"
	if err := auth.SaveV2Credentials(name, &after); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := w.run(ctx, &out); !isWatchTerminal(err) {
		t.Fatalf("wrong terminal error %v", err)
	}
	if !strings.Contains(out.String(), `"code":"identity_changed"`) {
		t.Fatalf("terminal diagnostic dropped: %s", out.String())
	}
}

func TestWatchTransportPreservesRequestDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	c := &http.Client{Timeout: 50 * time.Millisecond, Transport: watchTransport{context.Background()}}
	start := time.Now()
	if _, err := c.Get(srv.URL); err == nil {
		t.Fatal("hung request did not time out")
	}
	if time.Since(start) > time.Second {
		t.Fatal("request deadline was overwritten")
	}
}

func TestWatchNewPendingSetWakesImmediately(t *testing.T) {
	var count int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		id := "first"
		if count > 1 {
			id = "second"
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "data": map[string]interface{}{"pending_command_ids": []string{id}, "next_heartbeat_seconds": 30}})
	}))
	defer srv.Close()
	cfg, name := runtimeTestConfig(t, srv.URL, true)
	server, _ := cfg.GetActive(name)
	identity, _ := auth.LoadV2Credentials(name)
	w := &accountWatch{server: *server, identity: *identity, wake: make(chan struct{}, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	seen := []string{}
	w.emit = func(kind string, data interface{}) error {
		if kind == "control_pending" {
			ids := data.(map[string]interface{})["command_ids"].([]string)
			seen = append(seen, ids[0])
			if len(seen) == 1 {
				w.wake <- struct{}{}
			} else {
				cancel()
			}
		}
		return nil
	}
	_ = w.runtimeLoop(ctx)
	if len(seen) != 2 || seen[0] != "first" || seen[1] != "second" {
		t.Fatalf("new task suppressed: %v", seen)
	}
}
