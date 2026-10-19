package dispatch

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrMainNoticeUnsupported = errors.New("main_notice_transport_unverified")
var ErrMainNoticeBusy = errors.New("main_notice_target_busy")

// MainNoticeTarget is runtime host acceptance evidence, not a user-configurable
// permission bypass. No production host currently has verified hidden input.
type MainNoticeTarget struct {
	Host                string
	SessionID           string
	VerifiedHiddenInput bool
	Binding             Binding
}

type MainNoticeRunner struct{ Target MainNoticeTarget }

type MainNoticeCapabilityInfo struct {
	Protocol     string `json:"protocol"`
	Status       string `json:"status"`
	Experimental bool   `json:"experimental"`
}

func MainNoticeCapability(host string) MainNoticeCapabilityInfo {
	if host == "codex" {
		return MainNoticeCapabilityInfo{Protocol: "codex_turn", Status: "untested", Experimental: true}
	}
	return MainNoticeCapabilityInfo{Protocol: "acp", Status: "untested"}
}

// Run never creates a conversation or falls back to visible user input. The
// caller must treat any error as no notification and must not replay a turn
// whose admission or completion is uncertain.
func (r MainNoticeRunner) Run(ctx context.Context, prompt string) (Result, error) {
	t := r.Target
	if !t.VerifiedHiddenInput {
		return Result{}, ErrMainNoticeUnsupported
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(t.SessionID) == "" || len(t.SessionID) > 4096 || strings.HasPrefix(t.SessionID, "-") || strings.ContainsRune(t.SessionID, 0) || !utf8.ValidString(t.SessionID) {
		return Result{}, ErrMainSessionUnavailable
	}
	if strings.TrimSpace(prompt) == "" || len(prompt) > maxRunnerOutput || !utf8.ValidString(prompt) {
		return Result{}, errors.New("invalid_main_notice")
	}
	if t.Host == "" || t.Binding.Host != t.Host || t.Binding.MainSessionID != "" && t.Binding.MainSessionID != t.SessionID {
		return Result{}, ErrMainSessionMismatch
	}
	timeout := t.Binding.TimeoutSeconds
	if timeout <= 0 {
		timeout = 300
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	if t.Host == "codex" {
		return r.runCodex(ctx, prompt)
	}
	if t.Binding.Mode != "acp" {
		return Result{}, ErrMainNoticeUnsupported
	}
	return (Runner{Binding: t.Binding}).runACPWithOptions(ctx, Request{Prompt: prompt, SessionID: t.SessionID}, true)
}

// runCodex connects only to the already-running host through its official CLI
// proxy. It does not start a daemon or acquire, resume or replace a writer.
// additionalContext + empty input remains experimental and unverified in the
// desktop host; the acceptance gate above must stay closed until host testing.
// The schema also offers no atomic idle-only admission: acceptance testing must
// prove that concurrent user work cannot turn this request into a steering input.
func (r MainNoticeRunner) runCodex(ctx context.Context, prompt string) (Result, error) {
	cmd, err := (Runner{Binding: r.Target.Binding}).command([]string{"app-server", "proxy"})
	if err != nil {
		return Result{}, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, errors.New("main_notice_pipe_failed")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return Result{}, errors.New("main_notice_pipe_failed")
	}
	cmd.Stderr = io.Discard
	guard, err := startManaged(cmd)
	if err != nil {
		stdin.Close()
		stdout.Close()
		return Result{}, errors.New("main_notice_proxy_unavailable")
	}
	p := &mainNoticeRPC{ctx: ctx, stdin: stdin, incoming: make(chan acpRead, 16), stop: make(chan struct{})}
	defer func() {
		close(p.stop)
		stdin.Close()
		guard.kill()
		stdout.Close()
		_ = cmd.Wait()
		guard.close()
	}()
	go p.read(stdout)
	if _, err := p.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "eigenflux_main_notice", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}}); err != nil {
		return Result{}, err
	}
	if err := p.write(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return Result{}, err
	}
	raw, err := p.call("thread/read", map[string]any{"threadId": r.Target.SessionID, "includeTurns": false})
	if err != nil {
		return Result{}, err
	}
	var current struct {
		Thread struct {
			ID     string `json:"id"`
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
		} `json:"thread"`
	}
	if json.Unmarshal(raw, &current) != nil || current.Thread.ID != r.Target.SessionID {
		return Result{}, ErrMainSessionMismatch
	}
	if current.Thread.Status.Type != "idle" {
		return Result{}, ErrMainNoticeBusy
	}
	// Drop unsolicited pre-admission history. Nothing from a prior turn counts
	// as this request's execution or final result.
	p.pending = nil
	p.pendingBytes = 0
	contextEntries := map[string]any{"eigenflux_business_notice": map[string]string{"kind": "untrusted", "value": prompt}}
	if application, data, ok := strings.Cut(prompt, "\n\nUNTRUSTED COMMISSION EVENT:\n"); ok {
		contextEntries["eigenflux_notice_rules"] = map[string]string{"kind": "application", "value": application}
		contextEntries["eigenflux_business_notice"] = map[string]string{"kind": "untrusted", "value": data}
	}
	raw, err = p.call("turn/start", map[string]any{"threadId": r.Target.SessionID, "input": []any{}, "additionalContext": contextEntries})
	if err != nil {
		return Result{}, err
	}
	var started struct {
		Turn mainNoticeTurn `json:"turn"`
	}
	if json.Unmarshal(raw, &started) != nil || started.Turn.ID == "" || started.Turn.Status != "inProgress" {
		return Result{}, errors.New("main_notice_turn_unconfirmed")
	}
	return p.completed(r.Target.SessionID, started.Turn.ID)
}

type mainNoticeTurn struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Error  json.RawMessage `json:"error"`
}

type mainNoticeRPC struct {
	ctx          context.Context
	stdin        io.WriteCloser
	incoming     chan acpRead
	stop         chan struct{}
	nextID       int
	pending      []acpMessage
	pendingBytes int
}

func (p *mainNoticeRPC) read(reader io.Reader) {
	scan := bufio.NewScanner(reader)
	scan.Buffer(make([]byte, 4096), maxRunnerOutput+1)
	deliver := func(v acpRead) bool {
		select {
		case p.incoming <- v:
			return true
		case <-p.stop:
			return false
		}
	}
	for scan.Scan() {
		line := scan.Bytes()
		var msg acpMessage
		if len(line) > maxRunnerOutput || !utf8.Valid(line) || json.Unmarshal(line, &msg) != nil || msg.JSONRPC != "" && msg.JSONRPC != "2.0" {
			deliver(acpRead{err: errors.New("invalid_main_notice_message")})
			return
		}
		if !deliver(acpRead{message: msg}) {
			return
		}
	}
	deliver(acpRead{err: errors.New("main_notice_proxy_closed_or_oversize")})
}

func (p *mainNoticeRPC) write(value any) error {
	done := make(chan error, 1)
	go func() { done <- json.NewEncoder(p.stdin).Encode(value) }()
	select {
	case err := <-done:
		if err != nil {
			return errors.New("main_notice_write_failed")
		}
		return nil
	case <-p.ctx.Done():
		return p.ctx.Err()
	}
}

func (p *mainNoticeRPC) next() (acpMessage, error) {
	select {
	case <-p.ctx.Done():
		return acpMessage{}, p.ctx.Err()
	case value := <-p.incoming:
		if value.err != nil {
			return acpMessage{}, value.err
		}
		msg := value.message
		if msg.Method != "" && len(msg.ID) != 0 && string(msg.ID) != "null" {
			// Host requests are never approved by the background notice client.
			_ = p.write(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32601, "message": "Unsupported background request"}})
			return acpMessage{}, ErrNeedsUser
		}
		return msg, nil
	}
}

func (p *mainNoticeRPC) call(method string, params any) (json.RawMessage, error) {
	p.nextID++
	id := p.nextID
	if err := p.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		msg, err := p.next()
		if err != nil {
			return nil, err
		}
		if msg.Method != "" {
			p.pendingBytes += len(msg.Params) + len(msg.Method)
			if len(p.pending) >= 1024 || p.pendingBytes > maxRunnerOutput {
				return nil, errOutputLimit
			}
			p.pending = append(p.pending, msg)
			continue
		}
		var got int
		if json.Unmarshal(msg.ID, &got) != nil || got != id {
			return nil, errors.New("main_notice_response_mismatch")
		}
		if len(msg.Error) != 0 && string(msg.Error) != "null" {
			return nil, errors.New("main_notice_rpc_failed")
		}
		if len(msg.Result) == 0 || string(msg.Result) == "null" {
			return nil, errors.New("main_notice_missing_result")
		}
		return msg.Result, nil
	}
}

func (p *mainNoticeRPC) completed(session, turn string) (Result, error) {
	text := ""
	for {
		var msg acpMessage
		if len(p.pending) > 0 {
			msg, p.pending = p.pending[0], p.pending[1:]
		} else {
			var err error
			msg, err = p.next()
			if err != nil {
				return Result{}, err
			}
		}
		var event struct {
			ThreadID string         `json:"threadId"`
			TurnID   string         `json:"turnId"`
			Turn     mainNoticeTurn `json:"turn"`
			Item     struct {
				Type  string `json:"type"`
				Phase string `json:"phase"`
				Text  string `json:"text"`
			} `json:"item"`
		}
		if msg.Method != "item/completed" && msg.Method != "turn/completed" {
			continue
		}
		if json.Unmarshal(msg.Params, &event) != nil {
			return Result{}, errors.New("invalid_main_notice_receipt")
		}
		if event.ThreadID != session {
			return Result{}, ErrMainSessionMismatch
		}
		if msg.Method == "item/completed" {
			if event.TurnID != turn {
				return Result{}, errors.New("main_notice_turn_mismatch")
			}
			if event.Item.Type == "agentMessage" && event.Item.Phase == "final_answer" {
				text = event.Item.Text
			}
			continue
		}
		if event.Turn.ID != turn || event.Turn.Status != "completed" || len(event.Turn.Error) > 0 && string(event.Turn.Error) != "null" || strings.TrimSpace(text) == "" {
			return Result{}, errors.New("main_notice_completion_unconfirmed")
		}
		return Result{Text: text, SessionID: session, RunID: turn}, nil
	}
}
