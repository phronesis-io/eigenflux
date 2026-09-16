package cmd

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/controlcontext"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/profilestate"
	watchstate "cli.eigenflux.ai/internal/watch"
	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

var errWatchIdentity = errors.New("watch identity_changed: restart explicitly after account switching")
var errWatchConfiguration = errors.New("watch configuration_changed: restart explicitly after changing the server")
var errWatchOwnerReplaced = errors.New("watch owner_replaced: another owner is active")

func isWatchTerminal(err error) bool {
	return errors.Is(err, errWatchIdentity) || errors.Is(err, errWatchConfiguration) || errors.Is(err, errWatchOwnerReplaced)
}

type watchEvent struct {
	SchemaVersion string      `json:"schema_version"`
	Type          string      `json:"type"`
	EventID       string      `json:"event_id"`
	StateScope    string      `json:"state_scope"`
	Data          interface{} `json:"data"`
}

type accountWatch struct {
	home, scope   string
	server        config.Server
	identity      auth.V2Credentials
	emit          func(string, interface{}) error
	wsOnline      atomic.Bool
	runtimeOnline atomic.Bool
	ready         atomic.Bool
	adopted       atomic.Bool
	wake          chan struct{}
}

var watchCmd = &cobra.Command{
	Use: "watch", Short: "Run one account's PM, control and maintenance notification loop", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		server, err := cfg.GetActive(serverFlag)
		if err != nil {
			return err
		}
		home, err := watchstate.CanonicalHome(config.HomeDir())
		if err != nil {
			return err
		}
		release, err := watchstate.Acquire(home, server.Name)
		if err != nil {
			return err
		}
		defer release()
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		credentials, err := auth.LoadV2Credentials(server.Name)
		if err != nil {
			return err
		}
		credentials, err = refreshPinnedV2Credentials(ctx, server.Name, server.Endpoint, false, credentials.AgentID, credentials.PrincipalID)
		if err != nil {
			return err
		}
		w := &accountWatch{home: home, server: *server, identity: *credentials, wake: make(chan struct{}, 1)}
		w.scope = watchstate.Scope(home, server.Name, credentials.AgentID, credentials.PrincipalID)
		return w.run(ctx, cmd.OutOrStdout())
	},
}

func init() { rootCmd.AddCommand(watchCmd) }

func (w *accountWatch) run(parent context.Context, out io.Writer) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	events := make(chan watchEvent, 64)
	failures := make(chan error, 1)
	terminal := make(chan error, 1)
	fail := func(err error) {
		if isWatchTerminal(err) {
			select {
			case terminal <- err:
			default:
			}
		}
		select {
		case failures <- err:
		default:
		}
		cancel()
	}
	w.emit = func(kind string, data interface{}) error {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			fail(err)
			return err
		}
		e := watchEvent{"eigenflux_watch.v1", kind, hex.EncodeToString(id[:]), w.scope, data}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		select {
		case events <- e:
			return nil
		default:
			err := errors.New("watch consumer too slow; stopping to bound unread delivery")
			fail(err)
			return err
		}
	}
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		enc := json.NewEncoder(out)
		for event := range events {
			if err := enc.Encode(event); err != nil {
				fail(err)
				return
			}
		}
	}()
	if err := w.emit("started", w.runtimeData()); err != nil {
		return err
	}
	var workers sync.WaitGroup
	for _, run := range []func(context.Context) error{w.pmLoop, w.controlLoop, w.runtimeLoop, w.checkLoop, w.maintenanceLoop} {
		workers.Add(1)
		go func(run func(context.Context) error) {
			defer workers.Done()
			if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				fail(err)
			}
		}(run)
	}
	<-ctx.Done()
	workers.Wait()
	close(events)
	// Flush terminal diagnostics when possible; exit code 78 remains authoritative
	// when the host has stopped draining stdout.
	select {
	case <-writerDone:
	case <-time.After(250 * time.Millisecond):
	}
	select {
	case err := <-terminal:
		return err
	default:
	}
	select {
	case err := <-failures:
		return err
	default:
		return nil
	}
}

func (w *accountWatch) runtimeData() map[string]interface{} {
	return map[string]interface{}{"cli_version": version, "home": w.home, "server": w.server.Name, "agent_id": w.identity.AgentID, "principal_id": w.identity.PrincipalID, "pm_connected": w.wsOnline.Load(), "runtime_connected": w.runtimeOnline.Load()}
}
func (w *accountWatch) reportReady() {
	if w.wsOnline.Load() && w.runtimeOnline.Load() && w.ready.CompareAndSwap(false, true) {
		_ = w.emit("runtime_ready", w.runtimeData())
		if w.adopted.CompareAndSwap(false, true) {
			event := maintenance.NewEvent(maintenance.NewID(), "cli", "adoption", "adoption", "runtime_ready")
			event.ToVersion, event.RunningVersion = version, version
			meta := clientMetaForServer(&w.server)
			event.Host, event.Mode = runtimeProduct(meta.Host), meta.Mode
			_ = maintenance.Record(w.maintenanceScope(), event)
		}
	}
}
func (w *accountWatch) identityOK(c *auth.V2Credentials) bool {
	return c != nil && c.AgentID == w.identity.AgentID && c.PrincipalID == w.identity.PrincipalID
}
func (w *accountWatch) credentials(ctx context.Context, force bool) (*auth.V2Credentials, error) {
	// Check the persisted binding before any refresh can cause a remote side effect.
	c, err := auth.LoadV2Credentials(w.server.Name)
	if err != nil {
		return nil, err
	}
	if !w.identityOK(c) {
		return nil, errWatchIdentity
	}
	c, err = refreshPinnedV2Credentials(ctx, w.server.Name, w.server.Endpoint, force, w.identity.AgentID, w.identity.PrincipalID)
	if err != nil {
		return nil, err
	}
	if !w.identityOK(c) {
		return nil, errWatchIdentity
	}
	return c, nil
}
func watchPause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
func (w *accountWatch) diagnostic(area string, err error) error {
	code := "unavailable"
	if errors.Is(err, errWatchIdentity) {
		code = "identity_changed"
	} else if errors.Is(err, errWatchConfiguration) {
		code = "configuration_changed"
	} else if errors.Is(err, errWatchOwnerReplaced) {
		code = "owner_replaced"
	}
	var api *client.APIError
	if errors.As(err, &api) && api.ErrorCode != "" {
		code = api.ErrorCode
	}
	_ = w.emit("diagnostic", map[string]string{"area": area, "code": code})
	if isWatchTerminal(err) {
		return err
	}
	return nil
}

// Reuse stream's refresh/handshake classification and cursor wire format. The
// watch transport adds cancellation and a bounded event sink, not a subprocess.
func (w *accountWatch) pmLoop(ctx context.Context) error {
	cursor := ""
	backoff := reconnectMin
	for ctx.Err() == nil {
		credentials, err := w.credentials(ctx, false)
		if err == nil {
			u, parseErr := url.Parse(w.server.WSBaseURL() + "/api/v2/agent/events/ws")
			err = parseErr
			if err == nil {
				q := u.Query()
				if cursor != "" {
					q.Set("cursor", cursor)
				}
				u.RawQuery = q.Encode()
				head := http.Header{}
				head.Set("Authorization", "Bearer "+credentials.AccessToken)
				head.Set("X-CLI-Ver", version)
				clientMetaForServer(&w.server).SetHeaders(head)
				dialer := contextWatchDialer{ctx, websocket.Dialer{HandshakeTimeout: 10 * time.Second, Proxy: http.ProxyFromEnvironment}}
				var conn *websocket.Conn
				conn, _, _, err = dialStreamWithCredentialRefresh(dialer, u.String(), head, w.identity.AgentID, func() (*auth.V2Credentials, error) { return w.credentials(ctx, true) })
				if err == nil {
					w.wsOnline.Store(true)
					w.reportReady()
					backoff = reconnectMin
					stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
					conn.SetReadLimit(4 << 20)
					_ = conn.SetReadDeadline(time.Now().Add(pongWait))
					conn.SetPingHandler(func(string) error {
						_ = conn.SetReadDeadline(time.Now().Add(pongWait))
						return conn.WriteControl(websocket.PongMessage, nil, time.Now().Add(writeWait))
					})
					for ctx.Err() == nil {
						var raw []byte
						_, raw, err = conn.ReadMessage()
						if err != nil {
							break
						}
						var event struct {
							Type string          `json:"type"`
							Data json.RawMessage `json:"data"`
						}
						if json.Unmarshal(raw, &event) != nil || event.Type == "" {
							continue
						}
						var data struct {
							NextCursor string `json:"next_cursor"`
						}
						_ = json.Unmarshal(event.Data, &data)
						if data.NextCursor != "" {
							cursor = data.NextCursor
						}
						if emitErr := w.deliverPM(ctx, event.Type, event.Data); emitErr != nil {
							err = emitErr
							break
						}
					}
					stop()
					_ = conn.Close()
					w.wsOnline.Store(false)
					w.ready.Store(false)
					if websocket.IsCloseError(err, 4002) {
						return w.diagnostic("pm", errWatchOwnerReplaced)
					}
				}
			}
		}
		if err != nil {
			if fatal := w.diagnostic("pm", err); fatal != nil {
				return fatal
			}
		}
		if !watchPause(ctx, backoff) {
			break
		}
		backoff *= 2
		if backoff > reconnectMax {
			backoff = reconnectMax
		}
	}
	return ctx.Err()
}

// Account switching clears the same cache under the credential lock. Validate
// and write within that lock so a frame from the old socket cannot repopulate it.
func (w *accountWatch) deliverPM(ctx context.Context, kind string, data json.RawMessage) error {
	return auth.WithV2CredentialsLockContext(ctx, w.server.Name, 35*time.Second, func() error {
		credentials, err := auth.LoadV2Credentials(w.server.Name)
		if err != nil {
			return err
		}
		if !w.identityOK(credentials) {
			return errWatchIdentity
		}
		cacheMessagesForServer(data, w.server.Name)
		return w.emit(kind, data)
	})
}

type contextWatchDialer struct {
	ctx    context.Context
	dialer websocket.Dialer
}

func (d contextWatchDialer) Dial(u string, h http.Header) (*websocket.Conn, *http.Response, error) {
	return d.dialer.DialContext(d.ctx, u, h)
}

type watchTransport struct{ ctx context.Context }

func (t watchTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(t.ctx, cancel)
	if err := t.ctx.Err(); err != nil {
		stop()
		cancel()
		return nil, err
	}
	resp, err := http.DefaultTransport.RoundTrip(r.WithContext(ctx))
	if err != nil {
		stop()
		cancel()
		return nil, err
	}
	resp.Body = &watchResponseBody{ReadCloser: resp.Body, cleanup: func() { stop(); cancel() }}
	return resp, nil
}

type watchResponseBody struct {
	io.ReadCloser
	cleanup func()
}

func (b *watchResponseBody) Close() error { defer b.cleanup(); return b.ReadCloser.Close() }
func (w *accountWatch) api(ctx context.Context) (*client.Client, error) {
	cred, err := w.credentials(ctx, false)
	if err != nil {
		return nil, err
	}
	c := client.New(strings.TrimRight(w.server.Endpoint, "/")+"/api/v2", cred.AccessToken, version, clientMetaForServer(&w.server))
	c.HTTPClient = &http.Client{Timeout: 10 * time.Second, Transport: watchTransport{ctx}}
	c.OnUnauthorized = func() (string, error) {
		c, err := w.credentials(ctx, true)
		if err != nil {
			return "", err
		}
		return c.AccessToken, nil
	}
	return c, nil
}

func (w *accountWatch) controlLoop(ctx context.Context) error {
	for ctx.Err() == nil {
		c, err := w.api(ctx)
		if err == nil {
			req, reqErr := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/runtime/control/stream", nil)
			err = reqErr
			if err == nil {
				req.Header.Set("Authorization", "Bearer "+c.Token)
				req.Header.Set("X-CLI-Ver", version)
				c.Meta.SetHeaders(req.Header)
				// Bound the stream lifetime even if a proxy silently drops heartbeats.
				sseCtx, cancel := context.WithTimeout(ctx, 11*time.Minute)
				req = req.WithContext(sseCtx)
				idle := time.AfterFunc(65*time.Second, cancel)
				hc := &http.Client{}
				var resp *http.Response
				resp, err = hc.Do(req)
				if err == nil {
					if resp.StatusCode != 200 {
						err = streamHandshakeError(resp, errors.New("control stream unavailable"))
						resp.Body.Close()
						if resp.StatusCode == http.StatusUnauthorized {
							_, err = w.credentials(ctx, true)
						}
					} else {
						scanner := bufio.NewScanner(resp.Body)
						scanner.Buffer(make([]byte, 4096), 64<<10)
						for scanner.Scan() {
							idle.Reset(65 * time.Second)
							if strings.HasPrefix(scanner.Text(), "data:") {
								select {
								case w.wake <- struct{}{}:
								default:
								}
							}
						}
						err = scanner.Err()
						resp.Body.Close()
					}
				}
				idle.Stop()
				cancel()
			}
		}
		if err != nil {
			if fatal := w.diagnostic("control", err); fatal != nil {
				return fatal
			}
		}
		if !watchPause(ctx, reconnectMin) {
			break
		}
	}
	return ctx.Err()
}
func (w *accountWatch) runtimeLoop(ctx context.Context) error {
	runtimeID, err := runtimeInstanceID(w.server.Name)
	if err != nil {
		return err
	}
	delay := time.Duration(0)
	lastCommands := time.Time{}
	lastIDs := ""
	for ctx.Err() == nil {
		if delay > 0 {
			t := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-w.wake:
				t.Stop()
			case <-t.C:
			}
		}
		c, requestErr := w.api(ctx)
		delay = 30 * time.Second
		if requestErr == nil {
			var response *client.APIResponse
			request := map[string]interface{}{"runtime_instance_id": runtimeID, "capabilities": []string{"cli", "commands", "watch"}}
			if snapshot, err := controlcontext.Load(w.server.Name, w.identity.AgentID); err == nil && snapshot.Revision > 0 {
				request["applied_context_revision"] = snapshot.Revision
			}
			response, requestErr = c.Post("/runtime/heartbeat", request)
			if requestErr == nil {
				var data struct {
					IDs  []string `json:"pending_command_ids"`
					Next int      `json:"next_heartbeat_seconds"`
				}
				requestErr = json.Unmarshal(response.Data, &data)
				if requestErr == nil {
					w.runtimeOnline.Store(true)
					w.reportReady()
					if data.Next > 0 && data.Next <= 60 {
						delay = time.Duration(data.Next) * time.Second
					}
					sort.Strings(data.IDs)
					ids := strings.Join(data.IDs, "\x00")
					if len(data.IDs) > 0 && (ids != lastIDs || time.Since(lastCommands) >= time.Minute) {
						if err := w.emit("control_pending", map[string]interface{}{"command_ids": data.IDs}); err != nil {
							return err
						}
						lastCommands = time.Now()
					}
					lastIDs = ids
				}
			}
		}
		if requestErr != nil {
			w.runtimeOnline.Store(false)
			w.ready.Store(false)
			if fatal := w.diagnostic("runtime", requestErr); fatal != nil {
				return fatal
			}
		}
	}
	return ctx.Err()
}

func (w *accountWatch) checkLoop(ctx context.Context) error {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	lastProfile, lastMaintenance := time.Time{}, time.Time{}
	path, _ := os.Executable()
	initial, _ := os.Stat(path)
	firstCheck := true
	for ctx.Err() == nil {
		cred, err := auth.LoadV2Credentials(w.server.Name)
		if err != nil {
			return err
		}
		if !w.identityOK(cred) {
			_ = w.diagnostic("identity", errWatchIdentity)
			return errWatchIdentity
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		srv, err := cfg.GetActive(w.server.Name)
		if err != nil || srv.Endpoint != w.server.Endpoint || srv.WSBaseURL() != w.server.WSBaseURL() {
			return w.diagnostic("configuration", errWatchConfiguration)
		}
		now := time.Now()
		state := profilestate.Load(w.home, w.server.Name, w.identity.AgentID)
		if time.Since(lastProfile) >= 5*time.Minute && shouldPromptProfileRefresh(maxInt64(state.LastCheckedUnix, state.LastRefreshUnix), state.LastPromptedUnix, now.Unix()) {
			if err := w.emit("profile_review_due", map[string]string{"server": w.server.Name}); err != nil {
				return err
			}
			lastProfile = now
		}
		if state.LastCheckedUnix == 0 && state.LastRefreshUnix == 0 {
			_, _ = profilestate.Update(w.home, w.server.Name, w.identity.AgentID, func(s *profilestate.State) bool {
				if s.LastCheckedUnix != 0 || s.LastRefreshUnix != 0 {
					return false
				}
				s.LastCheckedUnix = now.Unix()
				return true
			})
		}
		if time.Since(lastMaintenance) >= 5*time.Minute && maintenanceDue(w.server.Name, now) {
			if err := w.emit("maintenance_due", map[string]string{"server": w.server.Name}); err != nil {
				return err
			}
			lastMaintenance = now
		}
		if current, statErr := os.Stat(path); statErr == nil && (firstCheck || initial == nil || !os.SameFile(initial, current) || current.ModTime() != initial.ModTime()) {
			probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			probe := exec.CommandContext(probeCtx, path, "--homedir", w.home, "--server", w.server.Name, "version", "--short")
			probe.Env = heartbeatReexecEnvironment(os.Environ())
			b, probeErr := probe.Output()
			cancel()
			if probeErr == nil && strings.TrimSpace(string(b)) != version {
				if err := w.emit("restart_required", map[string]string{"cli_version": strings.TrimSpace(string(b))}); err != nil {
					return err
				}
			}
			if probeErr == nil {
				initial = current
				firstCheck = false
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return ctx.Err()
}

func (w *accountWatch) maintenanceScope() maintenance.Scope {
	return maintenance.Scope{Home: w.home, Server: w.server.Name, AgentID: w.identity.AgentID}
}

func (w *accountWatch) maintenanceLoop(ctx context.Context) error {
	for ctx.Err() == nil {
		err := maintenance.Flush(w.maintenanceScope(), time.Now(), func(events []maintenance.Event) error {
			c, err := w.api(ctx)
			if err != nil {
				return err
			}
			c.HTTPClient.Timeout = 5 * time.Second
			_, err = c.Post("/maintenance/events:batch", map[string]interface{}{"events": events})
			return err
		})
		if isWatchTerminal(err) {
			return w.diagnostic("maintenance", err)
		}
		if !watchPause(ctx, time.Minute) {
			break
		}
	}
	return ctx.Err()
}
