// Package workbuddy bridges standard ACP to a WorkBuddy-owned local worker.
package workbuddy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const maxFrame = 4 << 20

var toolNamePattern = regexp.MustCompile(`^[A-Z][A-Za-z0-9_]*$`)

// Config selects a local installation. It never supplies model credentials.
type Config struct {
	ConfigDir, ControlSocket, CLIPath, Node string
	AllowedTools                            []string
	Tools                                   []string
}
type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type bridge struct {
	cfg                                                       Config
	out                                                       io.Writer
	mu                                                        sync.Mutex
	pendingMu                                                 sync.Mutex
	pending                                                   map[string]chan frame
	client                                                    *http.Client
	endpoint, password, connection, token, session, runtimeID string
	pendingSession                                            string
}

// Serve creates isolated host-owned sessions, not Desktop conversations.
func Serve(ctx context.Context, r io.Reader, w io.Writer, cfg Config) error {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		return errors.New("workbuddy_host_unsupported_platform")
	}
	return serve(ctx, r, w, cfg)
}
func serve(ctx context.Context, r io.Reader, w io.Writer, cfg Config) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cfg, err := resolve(cfg)
	if err != nil {
		return err
	}
	b := &bridge{cfg: cfg, out: w, pending: make(map[string]chan frame), client: &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect_disabled") }}}
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait(); b.cleanup() }()
	input := make(chan frame)
	readErr := make(chan error, 1)
	go func() {
		s := bufio.NewScanner(r)
		s.Buffer(make([]byte, 4096), maxFrame)
		for s.Scan() {
			var f frame
			if json.Unmarshal(s.Bytes(), &f) != nil {
				readErr <- errors.New("workbuddy_invalid_frame")
				return
			}
			select {
			case input <- f:
			case <-ctx.Done():
				return
			}
		}
		readErr <- s.Err()
	}()
	jobs := make(chan frame, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case f := <-jobs:
				if err := b.handle(ctx, f); err != nil {
					select {
					case readErr <- err:
					case <-ctx.Done():
					}
					return
				}
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			return err
		case f := <-input:
			if f.Method == "" {
				b.pendingMu.Lock()
				ch := b.pending[string(f.ID)]
				b.pendingMu.Unlock()
				if ch != nil {
					select {
					case ch <- f:
					case <-ctx.Done():
					}
				}
				continue
			}
			select {
			case jobs <- f:
			default:
				if len(f.ID) > 0 {
					if err := b.write(frame{JSONRPC: "2.0", ID: f.ID, Error: &rpcError{-32000, "workbuddy_busy"}}); err != nil {
						return err
					}
				}
			}
		}
	}

}
func (b *bridge) write(f frame) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return json.NewEncoder(b.out).Encode(f)
}
func raw(v any) json.RawMessage { x, _ := json.Marshal(v); return x }
func (b *bridge) reply(f frame, result any, err error) error {
	if len(f.ID) == 0 {
		return nil
	}
	if err != nil {
		return b.write(frame{JSONRPC: "2.0", ID: f.ID, Error: &rpcError{-32000, err.Error()}})
	}
	return b.write(frame{JSONRPC: "2.0", ID: f.ID, Result: raw(result)})
}
func (b *bridge) handle(ctx context.Context, f frame) error {
	switch f.Method {
	case "initialize":
		return b.reply(f, map[string]any{"protocolVersion": 1, "agentInfo": map[string]string{"name": "eigenflux-workbuddy-host", "version": "1"}, "agentCapabilities": map[string]any{"loadSession": false}}, nil)
	case "session/new":
		if b.session != "" || b.pendingSession != "" {
			return b.reply(f, nil, errors.New("workbuddy_session_already_created"))
		}
		var p struct {
			CWD string `json:"cwd"`
		}
		if json.Unmarshal(f.Params, &p) != nil {
			return b.reply(f, nil, errors.New("workbuddy_invalid_cwd"))
		}
		cwd, err := canonicalDir(p.CWD)
		if err != nil {
			return b.reply(f, nil, err)
		}
		if err = b.start(ctx, cwd); err != nil {
			return b.reply(f, nil, err)
		}
		return b.reply(f, map[string]string{"sessionId": b.session}, nil)
	case "session/load":
		return b.reply(f, nil, errors.New("workbuddy_existing_conversation_not_supported"))
	case "session/prompt":
		var p map[string]any
		if json.Unmarshal(f.Params, &p) != nil || p["sessionId"] != b.session || b.session == "" {
			return b.reply(f, nil, errors.New("workbuddy_invalid_session"))
		}
		p["_meta"] = map[string]any{"codebuddy.ai": map[string]any{"isMeta": true}}
		// Arbitrary launch configuration is never accepted from a prompt.
		for k := range p {
			if k != "sessionId" && k != "prompt" && k != "_meta" {
				delete(p, k)
			}
		}
		result, err := b.call(ctx, frame{JSONRPC: "2.0", ID: f.ID, Method: f.Method, Params: raw(p)}, true)
		return b.reply(f, json.RawMessage(result), err)
	case "session/cancel":
		if b.session != "" {
			_, _ = b.call(ctx, frame{JSONRPC: "2.0", Method: f.Method, Params: raw(map[string]string{"sessionId": b.session})}, false)
		}
		return nil
	default:
		return b.reply(f, nil, errors.New("workbuddy_method_not_supported"))
	}
}
func canonicalDir(s string) (string, error) {
	if !filepath.IsAbs(s) {
		return "", errors.New("workbuddy_invalid_cwd")
	}
	p, e := filepath.EvalSymlinks(s)
	if e != nil {
		return "", errors.New("workbuddy_invalid_cwd")
	}
	st, e := os.Stat(p)
	if e != nil || !st.IsDir() {
		return "", errors.New("workbuddy_invalid_cwd")
	}
	return p, nil
}
func resolve(c Config) (Config, error) {
	if c.ConfigDir == "" {
		h, e := os.UserHomeDir()
		if e != nil {
			return c, errors.New("workbuddy_home_unavailable")
		}
		c.ConfigDir = filepath.Join(h, ".workbuddy")
	}
	var err error
	c.ConfigDir, err = canonicalDir(c.ConfigDir)
	if err != nil {
		return c, errors.New("workbuddy_config_unavailable")
	}
	if c.ControlSocket == "" {
		c.ControlSocket, err = discoverControl(c.ConfigDir)
		if err != nil {
			return c, err
		}
	}
	if socketOwner(c.ControlSocket) != nil {
		return c, errors.New("workbuddy_untrusted_socket")
	}
	if c.CLIPath == "" {
		c.CLIPath = discoverCLI()
	}
	if !filepath.IsAbs(c.CLIPath) {
		return c, errors.New("workbuddy_invalid_cli")
	}
	if st, e := os.Stat(c.CLIPath); e != nil || !st.Mode().IsRegular() {
		return c, errors.New("workbuddy_cli_unavailable")
	}
	if c.Node == "" {
		c.Node, err = discoverNode(c.ConfigDir)
		if err != nil {
			return c, errors.New("workbuddy_node_unavailable")
		}
	}
	if !filepath.IsAbs(c.Node) {
		return c, errors.New("workbuddy_invalid_node")
	}
	for _, name := range c.Tools {
		if !toolNamePattern.MatchString(name) {
			return c, errors.New("workbuddy_invalid_tool_name")
		}
	}
	for _, v := range c.AllowedTools {
		if v == "" || strings.HasPrefix(v, "-") || strings.ContainsAny(v, "\r\n\x00") {
			return c, errors.New("workbuddy_invalid_tools")
		}
	}
	return c, nil
}
func sidecar(ctx context.Context, path, method string, params any) (json.RawMessage, error) {
	c, err := dialControl(ctx, path)
	if err != nil {
		return nil, errors.New("workbuddy_host_unavailable")
	}
	defer c.Close()
	deadline := time.Now().Add(45 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	if json.NewEncoder(c).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}) != nil {
		return nil, errors.New("workbuddy_host_write_failed")
	}
	s := bufio.NewScanner(c)
	s.Buffer(make([]byte, 4096), maxFrame)
	for s.Scan() {
		var f frame
		if json.Unmarshal(s.Bytes(), &f) != nil {
			return nil, errors.New("workbuddy_host_invalid_response")
		}
		if string(f.ID) != "1" {
			continue
		}
		if f.Error != nil {
			return nil, errors.New("workbuddy_host_request_failed")
		}
		return f.Result, nil
	}
	return nil, errors.New("workbuddy_host_response_missing")
}
func (b *bridge) start(ctx context.Context, cwd string) error {
	id := make([]byte, 16)
	if _, e := rand.Read(id); e != nil {
		return errors.New("workbuddy_random_failed")
	}
	id[6] = (id[6] & 15) | 64
	id[8] = (id[8] & 63) | 128
	sid := fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
	secret := make([]byte, 32)
	if _, e := rand.Read(secret); e != nil {
		return errors.New("workbuddy_random_failed")
	}
	b.password = hex.EncodeToString(secret)
	env := map[string]string{"PATH": os.Getenv("PATH"), "HOME": os.Getenv("HOME"), "CODEBUDDY_FORCE_LITE_WB_BUNDLE": "1", "CODEBUDDY_CONFIG_DIR": b.cfg.ConfigDir, "WORKBUDDY_CONFIG_DIR": b.cfg.ConfigDir, "WORKBUDDY_DATA_FOLDER_NAME": ".workbuddy", "CODEBUDDY_HOST": "workbuddy-desktop", "CODEBUDDY_INTERNET_ENVIRONMENT": "internal", "ACC_PRODUCT_CONFIG_PATH": filepath.Join(b.cfg.ConfigDir, "cache/acc-product-config-v3.json"), "CODEBUDDY_GATEWAY_AUTH_MODE": "password", "CODEBUDDY_GATEWAY_PASSWORD": b.password, "CODEBUDDY_GATEWAY_DISABLE_API_DOCS": "1", "DISABLE_AUTOUPDATER": "1", "CODEBUDDY_DISABLE_CRON": "1"}
	for _, k := range []string{"EIGENFLUX_HOME", "EIGENFLUX_SERVER", "EIGENFLUX_SKILLS_DIR", "EIGENFLUX_CDN_URL"} {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	if runtime.GOOS == "windows" {
		for _, k := range []string{"SystemRoot", "WINDIR", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "TEMP", "TMP", "COMSPEC", "PATHEXT", "ProgramFiles", "ProgramFiles(x86)"} {
			if v := os.Getenv(k); v != "" {
				env[k] = v
			}
		}
	}
	args := []string{b.cfg.CLIPath, "--serve", "--session-id", sid, "--permission-mode", "default"}
	if len(b.cfg.Tools) > 0 {
		args = append(args, "--tools", strings.Join(b.cfg.Tools, ","))
	}
	if len(b.cfg.AllowedTools) > 0 {
		args = append(args, "--allowedTools")
		args = append(args, b.cfg.AllowedTools...)
	}
	b.pendingSession = sid
	result, e := sidecar(ctx, b.cfg.ControlSocket, "session.create", map[string]any{"sessionId": sid, "command": b.cfg.Node, "args": args, "cwd": cwd, "env": env, "port": 0})
	if e != nil {
		// A definite native error did not create an owned runtime. A lost reply
		// may have left our random session running; cleanup recovers its ID.
		if e.Error() == "workbuddy_host_request_failed" {
			b.pendingSession = ""
		}
		return e
	}
	var created struct {
		SessionID string `json:"sessionId"`
		RuntimeID string `json:"runtimeId"`
		Endpoint  string `json:"acpEndpoint"`
	}
	if json.Unmarshal(result, &created) != nil || created.SessionID != sid || created.RuntimeID == "" {
		return errors.New("workbuddy_host_invalid_runtime")
	}
	b.session = sid
	b.runtimeID = created.RuntimeID
	b.pendingSession = ""
	endpoint, e := url.Parse(created.Endpoint)
	if e != nil || endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" || endpoint.Port() == "" || endpoint.Path != "/api/v1/acp" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("workbuddy_host_invalid_endpoint")
	}
	b.endpoint = endpoint.String()
	resp, e := b.request(ctx, "POST", b.endpoint+"/connect", nil)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	var creds struct {
		Connection string `json:"connectionId"`
		Token      string `json:"sessionToken"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, maxFrame)).Decode(&creds) != nil || creds.Connection == "" {
		return errors.New("workbuddy_connect_failed")
	}
	b.connection = creds.Connection
	b.token = creds.Token
	if _, e = b.call(ctx, frame{JSONRPC: "2.0", ID: raw("ef-initialize"), Method: "initialize", Params: raw(map[string]any{"protocolVersion": 1, "clientInfo": map[string]string{"name": "eigenflux-workbuddy-host", "version": "1"}, "clientCapabilities": map[string]any{}})}, false); e != nil {
		return e
	}
	_, e = b.call(ctx, frame{JSONRPC: "2.0", ID: raw("ef-load"), Method: "session/load", Params: raw(map[string]any{"sessionId": sid, "cwd": cwd, "mcpServers": []any{}})}, false)
	return e
}
func (b *bridge) request(ctx context.Context, method, endpoint string, body []byte) (*http.Response, error) {
	req, e := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if e != nil {
		return nil, errors.New("workbuddy_http_failed")
	}
	req.Header.Set("Authorization", "Bearer "+b.password)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("x-codebuddy-request", "1")
	if b.connection != "" {
		req.Header.Set("acp-connection-id", b.connection)
	}
	if b.token != "" {
		req.Header.Set("acp-session-token", b.token)
	}
	resp, e := b.client.Do(req)
	if e != nil {
		return nil, errors.New("workbuddy_http_failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, errors.New("workbuddy_http_rejected")
	}
	return resp, nil
}
func (b *bridge) call(ctx context.Context, f frame, forward bool) (json.RawMessage, error) {
	body, _ := json.Marshal(f)
	resp, e := b.request(ctx, "POST", b.endpoint, body)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		var reply frame
		if json.NewDecoder(io.LimitReader(resp.Body, maxFrame)).Decode(&reply) != nil {
			return nil, errors.New("workbuddy_invalid_response")
		}
		if reply.Method != "" || string(reply.ID) != string(f.ID) || (len(reply.Result) == 0 && reply.Error == nil) {
			return nil, errors.New("workbuddy_invalid_response")
		}
		if reply.Error != nil {
			return nil, errors.New("workbuddy_agent_rejected")
		}
		return reply.Result, nil
	}
	s := bufio.NewScanner(resp.Body)
	s.Buffer(make([]byte, 4096), maxFrame)
	for s.Scan() {
		line := s.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var reply frame
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &reply) != nil {
			return nil, errors.New("workbuddy_invalid_response")
		}
		if string(reply.ID) == string(f.ID) && reply.Method == "" {
			if reply.Error != nil {
				return nil, errors.New("workbuddy_agent_rejected")
			}
			return reply.Result, nil
		}
		if reply.Method == "notifications/sessionUpdate" {
			reply.Method = "session/update"
		}
		if forward && reply.Method != "" {
			if len(reply.ID) == 0 {
				if e = b.write(reply); e != nil {
					return nil, e
				}
			} else {
				if reply.Method != "session/request_permission" {
					return nil, errors.New("workbuddy_unsupported_agent_request")
				}
				key := string(reply.ID)
				ch := make(chan frame, 1)
				b.pendingMu.Lock()
				b.pending[key] = ch
				b.pendingMu.Unlock()
				if e = b.write(reply); e != nil {
					return nil, e
				}
				var answer frame
				select {
				case answer = <-ch:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				b.pendingMu.Lock()
				delete(b.pending, key)
				b.pendingMu.Unlock()
				answer.JSONRPC = "2.0"
				data, _ := json.Marshal(answer)
				r, e := b.request(ctx, "POST", b.endpoint, data)
				if e != nil {
					return nil, e
				}
				r.Body.Close()
			}
		}
	}
	return nil, errors.New("workbuddy_response_missing")
}

// recoverPendingRuntime only inspects the unguessable ID sent by this instance.
// Native reconnect exposes runtimeId even before readiness. Native pending-create
// cancellation has no nonce/CAS guard, so it is deliberately not used.
func (b *bridge) recoverPendingRuntime(ctx context.Context) {
	if b.pendingSession == "" {
		return
	}
	recovery, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for {
		result, err := sidecar(recovery, b.cfg.ControlSocket, "session.reconnect", map[string]string{"sessionId": b.pendingSession})
		if err == nil {
			var runtime struct {
				SessionID string `json:"sessionId"`
				RuntimeID string `json:"runtimeId"`
			}
			if json.Unmarshal(result, &runtime) == nil && runtime.SessionID == b.pendingSession && runtime.RuntimeID != "" {
				b.session = runtime.SessionID
				b.runtimeID = runtime.RuntimeID
				b.pendingSession = ""
				return
			}
		}
		select {
		case <-recovery.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}
func (b *bridge) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if b.runtimeID == "" {
		b.recoverPendingRuntime(ctx)
	}
	if b.runtimeID == "" || b.session == "" {
		return
	}
	if b.endpoint != "" && b.connection != "" {
		disconnect, cancelDisconnect := context.WithTimeout(ctx, 500*time.Millisecond)
		r, e := b.request(disconnect, "DELETE", b.endpoint, nil)
		if e == nil {
			r.Body.Close()
		}
		cancelDisconnect()
	}
	_, _ = sidecar(ctx, b.cfg.ControlSocket, "session.kill", map[string]string{"sessionId": b.session, "expectedRuntimeId": b.runtimeID})
}

// Discover resolves the existing local host without reading model credentials.
func Discover(cfg Config) (Config, error) { return resolve(cfg) }

// Probe creates and removes one isolated host runtime without calling the model.
func Probe(ctx context.Context, cfg Config, cwd string) error {
	cfg, err := resolve(cfg)
	if err != nil {
		return err
	}
	cwd, err = canonicalDir(cwd)
	if err != nil {
		return err
	}
	b := &bridge{cfg: cfg, out: io.Discard, pending: make(map[string]chan frame), client: &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect_disabled") }}}
	defer b.cleanup()
	return b.start(ctx, cwd)
}
