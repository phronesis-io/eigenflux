package dispatch

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrMainNoticeUnsupported = errors.New("main_notice_transport_unverified")
var ErrMainNoticeBusy = errors.New("main_notice_target_busy")

// MainNoticeTarget is runtime host acceptance evidence, not a user-configurable
// permission bypass. Production enables only the verified macOS owner transport.
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
		status := "untested"
		if MainNoticeHostVerified(host) {
			status = "verified_macos"
		}
		return MainNoticeCapabilityInfo{Protocol: "codex_turn", Status: status, Experimental: true}
	}
	return MainNoticeCapabilityInfo{Protocol: "acp", Status: "untested"}
}

// This is implementation acceptance, not an operator override. Other hosts and
// Windows remain disabled until their exact main-thread/UI paths are tested.
func MainNoticeHostVerified(host string) bool {
	return host == "codex" && runtime.GOOS == "darwin"
}

func MainNoticeBindingEnabled(binding Binding) bool {
	return MainNoticeHostVerified(binding.Host) && binding.Mode == "native" && binding.MainSessionID != "" && filepath.IsAbs(binding.Env["CODEX_HOME"])
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

type mainNoticeRPC struct {
	ctx      context.Context
	stdin    io.WriteCloser
	incoming chan acpRead
	stop     chan struct{}
	nextID   int
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
	notifications, notificationBytes := 0, 0
	for {
		msg, err := p.next()
		if err != nil {
			return nil, err
		}
		if msg.Method != "" {
			notificationBytes += len(msg.Params) + len(msg.Method)
			notifications++
			if notifications > 1024 || notificationBytes > maxRunnerOutput {
				return nil, errOutputLimit
			}
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
