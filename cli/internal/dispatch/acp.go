package dispatch

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type acpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}
type acpRead struct {
	message acpMessage
	err     error
}
type acpClient struct {
	ctx       context.Context
	stdin     io.WriteCloser
	incoming  chan acpRead
	stop      chan struct{}
	writeMu   sync.Mutex
	nextID    int
	sessionID string
	text      strings.Builder
}

func (r Runner) runACP(ctx context.Context, req Request) (Result, error) {
	cmd, err := r.command(r.Binding.Args)
	if err != nil {
		return Result{}, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, errors.New("acp_pipe_failed")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return Result{}, errors.New("acp_pipe_failed")
	}
	cmd.Stderr = io.Discard
	guard, err := startManaged(cmd)
	if err != nil {
		stdin.Close()
		stdout.Close()
		return Result{}, errors.New("runner_start_failed")
	}
	c := &acpClient{ctx: ctx, stdin: stdin, incoming: make(chan acpRead, 16), stop: make(chan struct{})}
	defer func() {
		close(c.stop)
		stdin.Close()
		guard.kill()
		stdout.Close()
		_ = cmd.Wait()
		guard.close()
	}()
	go c.read(stdout)
	raw, err := c.call("initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}, "clientInfo": map[string]string{"name": "eigenflux", "version": "1"}}, false)
	if err != nil {
		return Result{}, err
	}
	var init struct {
		ProtocolVersion   int `json:"protocolVersion"`
		AgentCapabilities struct {
			LoadSession bool `json:"loadSession"`
		} `json:"agentCapabilities"`
	}
	if json.Unmarshal(raw, &init) != nil || init.ProtocolVersion != 1 {
		return Result{}, errors.New("acp_protocol_version_mismatch")
	}
	params := map[string]any{"cwd": r.Binding.WorkDir, "mcpServers": []any{}}
	if req.SessionID != "" && init.AgentCapabilities.LoadSession {
		c.sessionID = req.SessionID
		params["sessionId"] = req.SessionID
		if _, err = c.call("session/load", params, false); err != nil {
			return Result{}, err
		}
	} else {
		raw, err = c.call("session/new", params, false)
		if err != nil {
			return Result{}, err
		}
		var session struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(raw, &session) != nil || session.SessionID == "" || len(session.SessionID) > 4096 {
			return Result{}, errors.New("acp_invalid_session")
		}
		c.sessionID = session.SessionID
	}
	raw, err = c.call("session/prompt", map[string]any{"sessionId": c.sessionID, "prompt": []any{map[string]string{"type": "text", "text": req.Prompt}}}, true)
	if err != nil {
		done := make(chan struct{})
		go func() {
			defer close(done)
			c.writeMu.Lock()
			defer c.writeMu.Unlock()
			_ = json.NewEncoder(stdin).Encode(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]string{"sessionId": c.sessionID}})
		}()
		select {
		case <-done:
		case <-time.After(50 * time.Millisecond):
		}
		return Result{}, err
	}
	var response struct {
		StopReason string `json:"stopReason"`
	}
	if json.Unmarshal(raw, &response) != nil || response.StopReason != "end_turn" {
		return Result{}, errors.New("acp_turn_incomplete")
	}
	if strings.TrimSpace(c.text.String()) == "" {
		return Result{}, errors.New("empty_agent_result")
	}
	return Result{Text: c.text.String(), SessionID: c.sessionID}, nil
}
func (c *acpClient) read(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxRunnerOutput+1)
	deliver := func(v acpRead) bool {
		select {
		case c.incoming <- v:
			return true
		case <-c.stop:
			return false
		}
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) > maxRunnerOutput {
			deliver(acpRead{err: errOutputLimit})
			return
		}
		if !utf8.Valid(line) {
			deliver(acpRead{err: errors.New("acp_invalid_encoding")})
			return
		}
		var msg acpMessage
		if json.Unmarshal(line, &msg) != nil || msg.JSONRPC != "2.0" {
			deliver(acpRead{err: errors.New("acp_invalid_message")})
			return
		}
		if !deliver(acpRead{message: msg}) {
			return
		}
	}
	err := scanner.Err()
	if err != nil {
		err = errors.New("acp_read_failed_or_message_too_large")
	} else {
		err = errors.New("acp_unexpected_exit")
	}
	deliver(acpRead{err: err})
}
func (c *acpClient) write(value any) error {
	done := make(chan error, 1)
	go func() { c.writeMu.Lock(); defer c.writeMu.Unlock(); done <- json.NewEncoder(c.stdin).Encode(value) }()
	select {
	case err := <-done:
		if err != nil {
			return errors.New("acp_write_failed")
		}
		return nil
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
}
func (c *acpClient) call(method string, params any, collect bool) (json.RawMessage, error) {
	c.nextID++
	id := c.nextID
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		select {
		case <-c.ctx.Done():
			return nil, c.ctx.Err()
		case value := <-c.incoming:
			if value.err != nil {
				return nil, value.err
			}
			msg := value.message
			if msg.Method != "" {
				if len(msg.ID) > 0 && string(msg.ID) != "null" {
					if msg.Method == "session/request_permission" {
						if err := c.write(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}}); err != nil {
							return nil, err
						}
						return nil, ErrNeedsUser
					}
					if err := c.write(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32601, "message": "Method not found"}}); err != nil {
						return nil, err
					}
				} else if collect && msg.Method == "session/update" {
					var p struct {
						SessionID string `json:"sessionId"`
						Update    struct {
							SessionUpdate string `json:"sessionUpdate"`
							Content       struct {
								Type string `json:"type"`
								Text string `json:"text"`
							} `json:"content"`
						} `json:"update"`
					}
					if json.Unmarshal(msg.Params, &p) != nil {
						return nil, errors.New("acp_invalid_update")
					}
					if p.SessionID == c.sessionID && p.Update.SessionUpdate == "agent_message_chunk" && p.Update.Content.Type == "text" {
						if c.text.Len()+len(p.Update.Content.Text) > maxRunnerOutput {
							return nil, errOutputLimit
						}
						c.text.WriteString(p.Update.Content.Text)
					}
				}
				continue
			}
			var responseID int
			if json.Unmarshal(msg.ID, &responseID) != nil || responseID != id {
				return nil, errors.New("acp_response_id_mismatch")
			}
			if len(msg.Error) > 0 && string(msg.Error) != "null" {
				return nil, errors.New("acp_rpc_error")
			}
			if len(msg.Result) == 0 {
				return nil, errors.New("acp_missing_result")
			}
			return msg.Result, nil
		}
	}
}
