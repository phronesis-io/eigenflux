package workbuddy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type hostFixture struct {
	path            string
	listener        net.Listener
	mu              sync.Mutex
	created         map[string]any
	killed          map[string]any
	createError     bool
	dropCreateReply bool
	reconnectSID    string
	endpoint        string
}

func newHost(t *testing.T, endpoint string) *hostFixture {
	t.Helper()
	dir, e := os.MkdirTemp("/tmp", "ef-wb-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "host.sock")
	l, e := net.Listen("unix", path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(path, 0600); e != nil {
		t.Fatal(e)
	}
	h := &hostFixture{path: path, listener: l, endpoint: endpoint}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				var q struct {
					ID     any            `json:"id"`
					Method string         `json:"method"`
					Params map[string]any `json:"params"`
				}
				if json.NewDecoder(c).Decode(&q) != nil {
					return
				}
				h.mu.Lock()
				var result any
				var fail bool
				switch q.Method {
				case "session.list":
					result = []map[string]string{{"sessionId": "unrelated-existing", "runtimeId": "other-runtime"}}
				case "session.create":
					h.created = q.Params
					fail = h.createError
					result = map[string]any{"sessionId": q.Params["sessionId"], "runtimeId": "owned-runtime", "acpEndpoint": h.endpoint}
				case "session.reconnect":
					h.reconnectSID, _ = q.Params["sessionId"].(string)
					if h.created != nil && h.created["sessionId"] == q.Params["sessionId"] {
						result = map[string]any{"sessionId": q.Params["sessionId"], "runtimeId": "owned-runtime"}
					} else {
						fail = true
					}
				case "session.kill":
					h.killed = q.Params
					result = map[string]bool{"ok": true}
				}
				drop := q.Method == "session.create" && h.dropCreateReply
				h.mu.Unlock()
				if drop {
					return
				}
				r := map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": result}
				if fail {
					r = map[string]any{"jsonrpc": "2.0", "id": q.ID, "error": map[string]any{"code": -32000, "message": "secret must not leak"}}
				}
				json.NewEncoder(c).Encode(r)
			}()
		}
	}()
	return h
}
func configFixture(t *testing.T, h *hostFixture) Config {
	t.Helper()
	d := t.TempDir()
	cli := filepath.Join(d, "cli")
	os.WriteFile(cli, []byte("dummy"), 0600)
	return Config{ConfigDir: d, ControlSocket: h.path, CLIPath: cli, Node: "/bin/true", AllowedTools: []string{"Read", "Bash"}, Tools: []string{"Read", "Write", "Edit"}}
}
func testBridge(c Config, w *bytes.Buffer) *bridge {
	return &bridge{cfg: c, out: w, pending: make(map[string]chan frame), client: &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("redirect disabled") }}}
}
func TestNativeWorkerUsesIsolatedIdentityAndHiddenPrompt(t *testing.T) {
	var mu sync.Mutex
	var hidden bool
	var received []string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			w.WriteHeader(204)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/connect") {
			json.NewEncoder(w).Encode(map[string]string{"connectionId": "connection", "sessionToken": "test-session-token"})
			return
		}
		var f frame
		if json.NewDecoder(r.Body).Decode(&f) != nil {
			t.Error("bad request")
			return
		}
		mu.Lock()
		received = append(received, f.Method)
		mu.Unlock()
		if f.Method == "session/prompt" {
			var p map[string]any
			json.Unmarshal(f.Params, &p)
			hidden = p["_meta"].(map[string]any)["codebuddy.ai"].(map[string]any)["isMeta"] == true
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", raw(map[string]any{"jsonrpc": "2.0", "method": "notifications/sessionUpdate", "params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "done"}}}}))
			fmt.Fprintf(w, "data: %s\n\n", raw(frame{JSONRPC: "2.0", ID: f.ID, Result: raw(map[string]string{"stopReason": "end_turn"})}))
			return
		}
		json.NewEncoder(w).Encode(frame{JSONRPC: "2.0", ID: f.ID, Result: raw(map[string]any{})})
	}))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	defer server.Close()
	h := newHost(t, server.URL+"/api/v1/acp")
	c := configFixture(t, h)
	var out bytes.Buffer
	b := testBridge(c, &out)
	t.Setenv("EIGENFLUX_HOME", "/fixture/eigenflux-home")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if e := b.start(ctx, t.TempDir()); e != nil {
		t.Fatal(e)
	}
	if e := b.handle(ctx, frame{JSONRPC: "2.0", ID: raw(7), Method: "session/prompt", Params: raw(map[string]any{"sessionId": b.session, "prompt": []any{map[string]string{"type": "text", "text": "test"}}, "env": map[string]string{"HOME": "attacker"}, "_meta": map[string]any{"codebuddy.ai": map[string]any{"isMeta": false}}})}); e != nil {
		t.Fatal(e)
	}
	b.cleanup()
	h.mu.Lock()
	defer h.mu.Unlock()
	if b.session == "unrelated-existing" || len(b.session) != 36 {
		t.Fatal("must create fresh uuid")
	}
	if h.killed["expectedRuntimeId"] != "owned-runtime" || h.killed["sessionId"] != b.session {
		t.Fatalf("unsafe cleanup: %#v", h.killed)
	}
	args := h.created["args"].([]any)
	joined := fmt.Sprint(args)
	if !strings.Contains(joined, "--tools Read,Write,Edit") || !strings.Contains(joined, "--allowedTools Read Bash") || !strings.Contains(joined, "--permission-mode default") {
		t.Fatalf("args: %s", joined)
	}
	env := h.created["env"].(map[string]any)
	if env["EIGENFLUX_HOME"] != "/fixture/eigenflux-home" || env["CODEBUDDY_GATEWAY_PASSWORD"] == "" {
		t.Fatal("missing host env")
	}
	if !hidden || (!strings.Contains(out.String(), "agent_message_chunk") || !strings.Contains(out.String(), `"method":"session/update"`) || strings.Contains(out.String(), "notifications/sessionUpdate")) {
		t.Fatal("hidden prompt/stream not forwarded")
	}
	if len(received) != 3 {
		t.Fatalf("unexpected replay/fallback: %#v", received)
	}
}
func TestCreateFailureNeverKillsExistingRuntime(t *testing.T) {
	h := newHost(t, "http://127.0.0.1:1/api/v1/acp")
	h.createError = true
	var out bytes.Buffer
	b := testBridge(configFixture(t, h), &out)
	e := b.start(context.Background(), t.TempDir())
	if e == nil || strings.Contains(e.Error(), "secret") {
		t.Fatalf("unredacted create error: %v", e)
	}
	b.cleanup()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.killed != nil {
		t.Fatal("failed create must not kill any runtime")
	}
}
func TestRejectRemoteEndpointStillCleansOwnedRuntime(t *testing.T) {
	h := newHost(t, "https://example.com/api/v1/acp")
	var out bytes.Buffer
	b := testBridge(configFixture(t, h), &out)
	e := b.start(context.Background(), t.TempDir())
	if e == nil || e.Error() != "workbuddy_host_invalid_endpoint" {
		t.Fatalf("unexpected error: %v", e)
	}
	b.cleanup()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.killed["expectedRuntimeId"] != "owned-runtime" {
		t.Fatal("owned worker leaked")
	}
}
func TestSocketRejectsSymlinksAndWritablePaths(t *testing.T) {
	h := newHost(t, "")
	link := filepath.Join(t.TempDir(), "host.sock")
	if e := os.Symlink(h.path, link); e != nil {
		t.Fatal(e)
	}
	if socketOwner(link) == nil {
		t.Fatal("symlink accepted")
	}
	os.Chmod(h.path, 0666)
	if socketOwner(h.path) == nil {
		t.Fatal("world writable socket accepted")
	}
}
func TestSidecarCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stall.sock")
	l, e := net.Listen("unix", path)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		c, e := l.Accept()
		if e == nil {
			defer c.Close()
			<-done
		}
	}()
	defer close(done)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, e = sidecar(ctx, path, "session.create", nil)
	if e == nil || time.Since(start) > time.Second {
		t.Fatalf("cancellation not bounded: %v", e)
	}
}

type callbackWriter struct{ fn func([]byte) }

func (w callbackWriter) Write(p []byte) (int, error) { w.fn(p); return len(p), nil }
func TestPermissionRequestWaitsForClientReply(t *testing.T) {
	var b *bridge
	permissionForwarded := false
	answerReceived := make(chan bool, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var f frame
		if json.NewDecoder(r.Body).Decode(&f) != nil {
			t.Error("invalid HTTP frame")
			return
		}
		if f.Method == "" {
			var result map[string]any
			json.Unmarshal(f.Result, &result)
			answerReceived <- result["outcome"] == "cancelled"
			w.WriteHeader(202)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", raw(frame{JSONRPC: "2.0", ID: raw("tool-1"), Method: "session/request_permission", Params: raw(map[string]any{"sessionId": "owned-session", "options": []any{}})}))
		w.(http.Flusher).Flush()
		select {
		case ok := <-answerReceived:
			if !ok {
				t.Error("permission decision changed")
			}
		case <-r.Context().Done():
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", raw(frame{JSONRPC: "2.0", ID: f.ID, Result: raw(map[string]string{"stopReason": "end_turn"})}))
	}))
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	server.Listener = l
	server.Start()
	defer server.Close()
	var unused bytes.Buffer
	b = testBridge(Config{}, &unused)
	b.endpoint = server.URL
	b.out = callbackWriter{func(p []byte) {
		var f frame
		if json.Unmarshal(p, &f) != nil {
			t.Error("invalid stdio frame")
			return
		}
		if f.Method != "session/request_permission" {
			t.Error("unexpected message")
			return
		}
		permissionForwarded = true
		b.pendingMu.Lock()
		ch := b.pending[string(f.ID)]
		b.pendingMu.Unlock()
		if ch == nil {
			t.Error("request not registered")
			return
		}
		ch <- frame{ID: f.ID, Result: raw(map[string]string{"outcome": "cancelled"})}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, e = b.call(ctx, frame{JSONRPC: "2.0", ID: raw(4), Method: "session/prompt", Params: raw(map[string]any{})}, true)
	if e != nil {
		t.Fatal(e)
	}
	if !permissionForwarded {
		t.Fatal("permission not forwarded")
	}
	if len(b.pending) != 0 {
		t.Fatal("pending permission leaked")
	}
}

func TestLostCreateReplyRecoversOnlyOwnedRuntime(t *testing.T) {
	h := newHost(t, "http://127.0.0.1:1/api/v1/acp")
	h.dropCreateReply = true
	var out bytes.Buffer
	b := testBridge(configFixture(t, h), &out)
	if err := b.start(context.Background(), t.TempDir()); err == nil {
		t.Fatal("lost reply accepted")
	}
	if b.runtimeID != "" || b.pendingSession == "" {
		t.Fatal("unexpected unknown startup state")
	}
	pending := b.pendingSession
	b.cleanup()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.reconnectSID != pending || h.killed["sessionId"] != pending || h.killed["expectedRuntimeId"] != "owned-runtime" {
		t.Fatal("recovery did not use exact owned runtime")
	}
}
func TestPendingCreateRecoveryDoesNotKillUnknownRuntime(t *testing.T) {
	h := newHost(t, "")
	var out bytes.Buffer
	b := testBridge(configFixture(t, h), &out)
	b.pendingSession = "this-instance-pending"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	b.recoverPendingRuntime(ctx)
	h.mu.Lock()
	defer h.mu.Unlock()
	if b.runtimeID != "" || h.killed != nil {
		t.Fatal("unknown pending create must not be killed")
	}
}

func TestStalledACPDisconnectCannotPreventRuntimeKill(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = l
	server.Start()
	defer server.Close()
	h := newHost(t, server.URL+"/api/v1/acp")
	var out bytes.Buffer
	b := testBridge(configFixture(t, h), &out)
	b.session = "owned-session"
	b.runtimeID = "owned-runtime"
	b.endpoint = server.URL + "/api/v1/acp"
	b.connection = "owned-connection"
	start := time.Now()
	b.cleanup()
	if time.Since(start) > 2*time.Second {
		t.Fatal("disconnect consumed cleanup budget")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.killed["expectedRuntimeId"] != "owned-runtime" {
		t.Fatal("stalled disconnect prevented guarded kill")
	}
}

func TestToolRestrictionRejectsRulesAndOptionInjection(t *testing.T) {
	h := newHost(t, "http://127.0.0.1:1")
	for _, name := range []string{"", "default", "--permission-mode", "Read,Write", "Bash(*)", "Read\nWrite"} {
		t.Run(name, func(t *testing.T) {
			c := configFixture(t, h)
			c.Tools = []string{name}
			if _, err := resolve(c); err == nil || err.Error() != "workbuddy_invalid_tool_name" {
				t.Fatalf("invalid tool restriction accepted: %q: %v", name, err)
			}
		})
	}
}
