// Package mainbridge adapts an explicitly configured local host owner to the
// EigenFlux ACP hidden-main extension. It never starts an Agent or a session.
package mainbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

const (
	mainExtension = "ai.eigenflux.mainConversation"
	maxFrame      = 2 << 20
	// This is a protocol check, not proof of an installed build or UI acceptance.
	hermesDesktopContract = 8
)

// HermesConfig contains only user-supplied routing. TokenFile is an optional
// absolute path containing the raw local gateway token; it is never discovered.
type HermesConfig struct {
	URL, SessionID, Cwd, TokenFile string
}

type rpcFrame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type peerRead struct {
	frame rpcFrame
	err   error
}

type hermesPeer struct {
	conn       *websocket.Conn
	ctx        context.Context
	cancel     context.CancelFunc
	incoming   chan peerRead
	nextID     int
	config     HermesConfig
	version    string
	versionSet bool
}

func validID(value string) bool {
	if value == "" || len(value) > 4096 || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

func validateHermesConfig(config HermesConfig) (*url.URL, error) {
	u, err := url.Parse(config.URL)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.User != nil || u.Fragment != "" || u.RawQuery != "" ||
		u.Path != "/api/ws" || u.RawPath != "" || !validID(config.SessionID) || !filepath.IsAbs(config.Cwd) || filepath.Clean(config.Cwd) != config.Cwd {
		return nil, errors.New("hermes_main_invalid_config")
	}
	host := u.Hostname()
	if host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback()) {
		return nil, errors.New("hermes_main_requires_loopback")
	}
	if port, err := strconv.Atoi(u.Port()); err != nil || port < 1 || port > 65535 {
		return nil, errors.New("hermes_main_invalid_config")
	}
	if config.TokenFile != "" {
		if !filepath.IsAbs(config.TokenFile) {
			return nil, errors.New("hermes_main_invalid_token_file")
		}
		file, err := os.Open(config.TokenFile)
		if err != nil {
			return nil, errors.New("hermes_main_token_unavailable")
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 8192 {
			return nil, errors.New("hermes_main_invalid_token_file")
		}
		token, err := io.ReadAll(io.LimitReader(file, 8193))
		value := strings.TrimSpace(string(token))
		if err != nil || len(token) > 8192 || !validID(value) {
			return nil, errors.New("hermes_main_invalid_token_file")
		}
		query := u.Query()
		query.Set("token", value)
		u.RawQuery = query.Encode()
	}
	return u, nil
}

func dialHermes(ctx context.Context, config HermesConfig) (*hermesPeer, error) {
	u, err := validateHermesConfig(config)
	if err != nil {
		return nil, err
	}
	// No proxy or DNS can redirect an explicit local endpoint or its token.
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, NetDialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if host == "localhost" {
			host = "127.0.0.1"
		}
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(host, port))
	}}
	conn, response, err := dialer.DialContext(ctx, u.String(), http.Header{})
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		return nil, errors.New("hermes_main_connection_failed")
	}
	peerCtx, cancel := context.WithCancel(ctx)
	p := &hermesPeer{conn: conn, ctx: peerCtx, cancel: cancel, incoming: make(chan peerRead, 32), config: config}
	conn.SetReadLimit(maxFrame)
	go p.read()
	go func() { <-peerCtx.Done(); conn.Close() }()
	return p, nil
}

func (p *hermesPeer) close() {
	p.cancel()
	p.conn.Close()
}

func (p *hermesPeer) read() {
	deliver := func(value peerRead) bool {
		select {
		case p.incoming <- value:
			return true
		case <-p.ctx.Done():
			return false
		}
	}
	for {
		kind, data, err := p.conn.ReadMessage()
		if err != nil {
			deliver(peerRead{err: errors.New("hermes_main_connection_closed")})
			return
		}
		if kind != websocket.TextMessage || !utf8.Valid(data) {
			deliver(peerRead{err: errors.New("hermes_main_invalid_frame")})
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		for {
			var frame rpcFrame
			if err := decoder.Decode(&frame); err != nil {
				if err != io.EOF {
					deliver(peerRead{err: errors.New("hermes_main_invalid_frame")})
					return
				}
				break
			}
			if frame.JSONRPC != "2.0" {
				deliver(peerRead{err: errors.New("hermes_main_invalid_frame")})
				return
			}
			if !deliver(peerRead{frame: frame}) {
				return
			}
		}
	}
}

func (p *hermesPeer) receive(ctx context.Context) (rpcFrame, error) {
	select {
	case <-ctx.Done():
		return rpcFrame{}, ctx.Err()
	case value := <-p.incoming:
		return value.frame, value.err
	}
}

type hermesEvent struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id"`
	Payload   json.RawMessage `json:"payload"`
}

func (p *hermesPeer) ready(ctx context.Context) error {
	for {
		frame, err := p.receive(ctx)
		if err != nil {
			return err
		}
		var event hermesEvent
		if frame.Method != "event" || json.Unmarshal(frame.Params, &event) != nil || event.Type != "gateway.ready" {
			return errors.New("hermes_main_unsupported_protocol")
		}
		var ready struct {
			SessionAuthority bool `json:"session_authority"`
		}
		if json.Unmarshal(event.Payload, &ready) != nil || ready.SessionAuthority {
			return errors.New("hermes_main_unsupported_protocol")
		}
		return nil
	}
}

// call never retries a request or answers a host approval on the user's behalf.
// Completion may arrive before the submit response, so preserve only this
// explicitly bound session's completions during that one submit.
func (p *hermesPeer) call(ctx context.Context, method string, params any, capture bool) (json.RawMessage, []json.RawMessage, error) {
	p.nextID++
	id := p.nextID
	deadline := time.Now().Add(5 * time.Second)
	if until, ok := ctx.Deadline(); ok && until.Before(deadline) {
		deadline = until
	}
	p.conn.SetWriteDeadline(deadline)
	if p.conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}) != nil {
		return nil, nil, errors.New("hermes_main_write_failed")
	}
	var pending []json.RawMessage
	for {
		frame, err := p.receive(ctx)
		if err != nil {
			return nil, nil, err
		}
		if frame.Method != "" {
			if len(frame.ID) > 0 && string(frame.ID) != "null" {
				return nil, nil, errors.New("hermes_main_requires_user")
			}
			var event hermesEvent
			if capture && frame.Method == "event" && json.Unmarshal(frame.Params, &event) == nil && event.Type == "message.complete" && event.SessionID == p.config.SessionID {
				if len(pending) >= 8 {
					return nil, nil, errors.New("hermes_main_response_unconfirmed")
				}
				pending = append(pending, event.Payload)
			}
			continue
		}
		if string(frame.ID) != strconv.Itoa(id) {
			return nil, nil, errors.New("hermes_main_response_unconfirmed")
		}
		if len(frame.Error) > 0 && string(frame.Error) != "null" {
			return nil, nil, errors.New("hermes_main_rpc_failed")
		}
		if len(frame.Result) == 0 {
			return nil, nil, errors.New("hermes_main_response_unconfirmed")
		}
		return frame.Result, pending, nil
	}
}

func (p *hermesPeer) inspect(ctx context.Context) (string, error) {
	raw, _, err := p.call(ctx, "session.active_list", map[string]any{}, false)
	if err != nil {
		return "", err
	}
	var active struct {
		Sessions []struct{ ID, Status string } `json:"sessions"`
	}
	if json.Unmarshal(raw, &active) != nil {
		return "", errors.New("hermes_main_response_unconfirmed")
	}
	found := 0
	for _, row := range active.Sessions {
		if row.ID == p.config.SessionID {
			found++
			if !validHermesStatus(row.Status) {
				return "", errors.New("hermes_main_unsupported_protocol")
			}
		}
	}
	if found != 1 {
		return "", errors.New("hermes_main_session_unavailable")
	}
	raw, _, err = p.call(ctx, "session.activate", map[string]any{"session_id": p.config.SessionID, "omit_messages": true}, false)
	if err != nil {
		return "", err
	}
	var activated struct {
		SessionID       string            `json:"session_id"`
		Status          string            `json:"status"`
		Running         *bool             `json:"running"`
		MessagesOmitted bool              `json:"messages_omitted"`
		Messages        []json.RawMessage `json:"messages"`
		Info            struct {
			Cwd, Version    string
			DesktopContract int `json:"desktop_contract"`
		} `json:"info"`
	}
	if json.Unmarshal(raw, &activated) != nil || activated.SessionID != p.config.SessionID || activated.Info.Cwd != p.config.Cwd ||
		activated.Info.DesktopContract != hermesDesktopContract || !activated.MessagesOmitted || len(activated.Messages) != 0 ||
		activated.Running == nil || !validHermesStatus(activated.Status) || (activated.Status == "idle" && *activated.Running) {
		return "", errors.New("hermes_main_unsupported_protocol")
	}
	if p.versionSet && activated.Info.Version != p.version {
		return "", errors.New("hermes_main_backend_changed")
	}
	p.version, p.versionSet = activated.Info.Version, true
	if activated.Status != "idle" || *activated.Running {
		return "busy", nil
	}
	return "idle", nil
}

func validHermesStatus(status string) bool {
	return status == "idle" || status == "working" || status == "starting" || status == "waiting"
}

func hermesCompletion(payload json.RawMessage, userRow int64) (string, string, bool) {
	var completed struct {
		Text, Status  string
		PersistedTurn struct {
			Complete   bool    `json:"complete"`
			UserRowID  int64   `json:"user_row_id"`
			FinalRowID int64   `json:"final_assistant_row_id"`
			RowIDs     []int64 `json:"row_ids"`
		} `json:"persisted_turn"`
	}
	if json.Unmarshal(payload, &completed) != nil || completed.Status != "complete" || !completed.PersistedTurn.Complete ||
		completed.PersistedTurn.UserRowID != userRow || completed.PersistedTurn.FinalRowID <= 0 || completed.PersistedTurn.FinalRowID == userRow ||
		strings.TrimSpace(completed.Text) == "" || !utf8.ValidString(completed.Text) || len(completed.Text) > 1<<20 {
		return "", "", false
	}
	seen := make(map[int64]bool)
	for _, row := range completed.PersistedTurn.RowIDs {
		if row <= 0 || seen[row] {
			return "", "", false
		}
		seen[row] = true
	}
	if !seen[userRow] || !seen[completed.PersistedTurn.FinalRowID] {
		return "", "", false
	}
	return completed.Text, fmt.Sprintf("hermes-row-%d-%d", userRow, completed.PersistedTurn.FinalRowID), true
}

func (p *hermesPeer) notify(ctx context.Context, prompt string) (string, string, error) {
	state, err := p.inspect(ctx)
	if err != nil {
		return "", "", err
	}
	if state != "idle" {
		return "", "", errors.New("hermes_main_busy")
	}
	raw, pending, err := p.call(ctx, "prompt.submit", map[string]any{
		"session_id": p.config.SessionID, "text": prompt, "display_kind": "hidden", "queued": true,
	}, true)
	if err != nil {
		return "", "", err
	}
	var submitted struct {
		Status    string `json:"status"`
		UserRowID int64  `json:"user_row_id"`
	}
	if json.Unmarshal(raw, &submitted) != nil || submitted.Status != "streaming" || submitted.UserRowID <= 0 {
		return "", "", errors.New("hermes_main_response_unconfirmed")
	}
	for _, payload := range pending {
		if text, run, ok := hermesCompletion(payload, submitted.UserRowID); ok {
			return text, run, nil
		}
	}
	for {
		frame, err := p.receive(ctx)
		if err != nil {
			return "", "", err
		}
		if frame.Method != "" && len(frame.ID) > 0 && string(frame.ID) != "null" {
			return "", "", errors.New("hermes_main_requires_user")
		}
		var event hermesEvent
		if frame.Method == "event" && json.Unmarshal(frame.Params, &event) == nil && event.Type == "message.complete" && event.SessionID == p.config.SessionID {
			if text, run, ok := hermesCompletion(event.Payload, submitted.UserRowID); ok {
				return text, run, nil
			}
		}
	}
}

type clientParams struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	RequestID string `json:"requestId,omitempty"`
	Prompt    string `json:"prompt,omitempty"`
}

// ServeHermes serves only the hidden-main ACP extension. The caller owns process
// lifetime and input cancellation. One notify ends this bridge connection;
// disconnecting never cancels or replays an admitted host turn.
func ServeHermes(ctx context.Context, in io.Reader, out io.Writer, config HermesConfig) error {
	if _, err := validateHermesConfig(config); err != nil {
		return err
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), maxFrame)
	encoder := json.NewEncoder(out)
	var peer *hermesPeer
	defer func() {
		if peer != nil {
			peer.close()
		}
	}()
	respond := func(id json.RawMessage, result any, problem error) error {
		value := map[string]any{"jsonrpc": "2.0", "id": id}
		if problem != nil {
			value["error"] = map[string]any{"code": -32000, "message": problem.Error()}
		} else {
			value["result"] = result
		}
		return encoder.Encode(value)
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var request rpcFrame
		if !utf8.Valid(scanner.Bytes()) || json.Unmarshal(scanner.Bytes(), &request) != nil || request.JSONRPC != "2.0" {
			return errors.New("hermes_main_invalid_client_request")
		}
		if len(request.ID) == 0 || string(request.ID) == "null" {
			if request.Method == "initialized" {
				continue
			}
			return errors.New("hermes_main_invalid_client_request")
		}
		if request.Method == "initialize" && peer == nil {
			var init struct {
				ProtocolVersion int `json:"protocolVersion"`
			}
			if json.Unmarshal(request.Params, &init) != nil || init.ProtocolVersion != 1 {
				return respond(request.ID, nil, errors.New("hermes_main_unsupported_protocol"))
			}
			setupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			var err error
			peer, err = dialHermes(ctx, config)
			if err == nil {
				err = peer.ready(setupCtx)
			}
			if err == nil {
				_, err = peer.inspect(setupCtx)
			}
			cancel()
			if err != nil {
				return respond(request.ID, nil, err)
			}
			capability := map[string]any{"version": 1, "host": "hermes", "existingSession": true, "hiddenInput": true, "resultOnly": true}
			if err := respond(request.ID, map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"_meta": map[string]any{mainExtension: capability}}}, nil); err != nil {
				return err
			}
			continue
		}
		var params clientParams
		decoder := json.NewDecoder(bytes.NewReader(request.Params))
		decoder.DisallowUnknownFields()
		if peer == nil || decoder.Decode(&params) != nil || params.SessionID != config.SessionID || params.Cwd != config.Cwd {
			return respond(request.ID, nil, errors.New("hermes_main_target_mismatch"))
		}
		switch request.Method {
		case "_eigenflux/main/inspect":
			inspectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			state, err := peer.inspect(inspectCtx)
			cancel()
			if err != nil {
				return respond(request.ID, nil, err)
			}
			if err := respond(request.ID, map[string]any{"sessionId": config.SessionID, "host": "hermes", "cwd": config.Cwd, "state": state, "existingSession": true, "hiddenInput": true, "resultOnly": true}, nil); err != nil {
				return err
			}
		case "_eigenflux/main/notify":
			if !validID(params.RequestID) || strings.TrimSpace(params.Prompt) == "" || len(params.Prompt) > 1<<20 {
				return respond(request.ID, nil, errors.New("hermes_main_invalid_client_request"))
			}
			turnCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			text, run, err := peer.notify(turnCtx, params.Prompt)
			cancel()
			if err != nil {
				return respond(request.ID, nil, err)
			}
			return respond(request.ID, map[string]any{"sessionId": config.SessionID, "requestId": params.RequestID, "runId": run, "stopReason": "end_turn", "text": text, "existingSession": true, "hiddenInput": true, "resultOnly": true}, nil)
		default:
			return respond(request.ID, nil, errors.New("hermes_main_unsupported_method"))
		}
	}
	if scanner.Err() != nil {
		return errors.New("hermes_main_invalid_client_request")
	}
	return nil
}
