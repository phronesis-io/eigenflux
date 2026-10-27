package dispatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

type mainNoticeTurn struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Error  json.RawMessage `json:"error"`
	Items  []struct {
		Type  string `json:"type"`
		Phase string `json:"phase"`
		Text  string `json:"text"`
	} `json:"items"`
}

// The desktop owner retains the writer. The separate App Server below only
// reads persistent receipts; it never resumes a thread or starts a turn.
func openMainNoticeReader(ctx context.Context, binding Binding) (*mainNoticeRPC, func(), error) {
	cmd, err := (Runner{Binding: binding}).command([]string{"app-server", "--stdio"})
	if err != nil {
		return nil, nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, errors.New("main_notice_reader_unavailable")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, nil, errors.New("main_notice_reader_unavailable")
	}
	cmd.Stderr = io.Discard
	guard, err := startManaged(cmd)
	if err != nil {
		stdin.Close()
		stdout.Close()
		return nil, nil, errors.New("main_notice_reader_unavailable")
	}
	p := &mainNoticeRPC{ctx: ctx, stdin: stdin, incoming: make(chan acpRead, 16), stop: make(chan struct{})}
	closeReader := func() {
		close(p.stop)
		stdin.Close()
		guard.kill()
		stdout.Close()
		_ = cmd.Wait()
		guard.close()
	}
	go p.read(stdout)
	if _, err := p.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "eigenflux_main_notice_readback", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}}); err != nil {
		closeReader()
		return nil, nil, err
	}
	if err := p.write(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		closeReader()
		return nil, nil, err
	}
	return p, closeReader, nil
}

func mainNoticeOwner(ctx context.Context, ipc *codexOwnerIPC, session string) (string, error) {
	raw, owner, err := ipc.call(ctx, "thread-owner-discovery", 1, map[string]string{"hostId": "local", "conversationId": session}, "")
	if err != nil {
		return "", err
	}
	var capability struct {
		UntrustedInput bool `json:"supportsUntrustedAppInput"`
	}
	if owner == "" || json.Unmarshal(raw, &capability) != nil || !capability.UntrustedInput {
		return "", ErrMainNoticeUnsupported
	}
	return owner, nil
}

func (p *mainNoticeRPC) mainNoticeTurns(session string, limit int, itemsView string) ([]mainNoticeTurn, error) {
	raw, err := p.call("thread/turns/list", map[string]any{"threadId": session, "limit": limit, "sortDirection": "desc", "itemsView": itemsView})
	if err != nil {
		return nil, err
	}
	var page struct {
		Data []mainNoticeTurn `json:"data"`
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields["data"]) == 0 || string(fields["data"]) == "null" || json.Unmarshal(raw, &page) != nil || len(page.Data) > limit {
		return nil, errors.New("main_notice_receipts_invalid")
	}
	for _, turn := range page.Data {
		if turn.ID == "" || turn.Status == "" {
			return nil, errors.New("main_notice_receipts_invalid")
		}
	}
	return page.Data, nil
}

func (p *mainNoticeRPC) checkMainNoticeTarget(session string) (map[string]bool, error) {
	raw, err := p.call("thread/read", map[string]any{"threadId": session, "includeTurns": false})
	if err != nil {
		return nil, err
	}
	var current struct {
		Thread struct {
			ID       string `json:"id"`
			Archived bool   `json:"archived"`
			Status   struct {
				Type string `json:"type"`
			} `json:"status"`
		} `json:"thread"`
	}
	if json.Unmarshal(raw, &current) != nil || current.Thread.ID != session || current.Thread.Archived {
		return nil, ErrMainSessionMismatch
	}
	switch current.Thread.Status.Type {
	case "active":
		return nil, ErrMainNoticeBusy
	case "idle", "notLoaded":
	default:
		return nil, errors.New("main_notice_target_unavailable")
	}
	turns, err := p.mainNoticeTurns(session, 8, "notLoaded")
	if err != nil {
		return nil, err
	}
	if len(turns) > 0 && turns[0].Status == "inProgress" {
		return nil, ErrMainNoticeBusy
	}
	seen := make(map[string]bool, len(turns))
	for _, turn := range turns {
		seen[turn.ID] = true
	}
	return seen, nil
}

func mainNoticeUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errors.New("main_notice_id_unavailable")
	}
	value[6], value[8] = (value[6]&0x0f)|0x40, (value[8]&0x3f)|0x80
	raw := hex.EncodeToString(value[:])
	return raw[:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:], nil
}

func (r MainNoticeRunner) runCodex(ctx context.Context, prompt string) (Result, error) {
	application, data, ok := strings.Cut(prompt, "\n\nUNTRUSTED COMMISSION EVENT:\n")
	if !ok || strings.TrimSpace(application) == "" || !json.Valid([]byte(data)) {
		return Result{}, errors.New("invalid_main_notice_envelope")
	}
	return r.runCodexEnvelope(ctx, application, data, false)
}

// Both lanes use the Desktop owner. Only PM decisions may finish silently.
func (r MainNoticeRunner) runCodexEnvelope(ctx context.Context, application, data string, allowSilent bool) (Result, error) {
	ipc, err := openCodexOwnerIPC(ctx, r.Target.Binding)
	if err != nil {
		return Result{}, mainNoticeNotSubmitted(err)
	}
	defer ipc.Close()
	return r.runCodexEnvelopeOnIPC(ctx, ipc, application, data, allowSilent)
}

func (r MainNoticeRunner) runCodexEnvelopeOnIPC(ctx context.Context, ipc *codexOwnerIPC, application, data string, allowSilent bool) (Result, error) {
	owner, err := mainNoticeOwner(ctx, ipc, r.Target.SessionID)
	if err != nil {
		return Result{}, mainNoticeNotSubmitted(err)
	}
	reader, closeReader, err := openMainNoticeReader(ctx, r.Target.Binding)
	if err != nil {
		return Result{}, mainNoticeNotSubmitted(err)
	}
	defer closeReader()
	baseline, err := reader.checkMainNoticeTarget(r.Target.SessionID)
	if err != nil {
		return Result{}, mainNoticeNotSubmitted(err)
	}
	id, err := mainNoticeUUID()
	if err != nil {
		return Result{}, err
	}
	callID := "untrusted_input_" + id
	request := map[string]any{
		"threadId": r.Target.SessionID, "clientUserMessageId": id, "input": []any{},
		"additionalContext": map[string]any{
			"eigenflux_notice_rules":    map[string]string{"kind": "application", "value": application},
			"eigenflux_business_notice": map[string]string{"kind": "untrusted", "value": data},
		},
	}
	// This is the desktop's native untrusted-app input shape. Nonempty
	// responseItems activates its busy-thread checks; no user prompt is created.
	// Its inject_items -> turn/start sequence is not an atomic idle-only API.
	responseItems := []any{
		map[string]any{"type": "function_call", "call_id": callID, "name": "untrusted_input", "arguments": "{}"},
		map[string]any{"type": "function_call_output", "call_id": callID, "output": []any{map[string]string{"type": "input_text", "text": data}}},
	}
	raw, handledBy, err := ipc.call(ctx, "thread-follower-start-turn", 2, map[string]any{
		"conversationId": r.Target.SessionID,
		"turnStart":      map[string]any{"request": request, "context": map[string]any{"inheritThreadSettings": true, "responseItems": responseItems}},
	}, owner)
	if err != nil {
		return Result{}, err
	}
	var response struct {
		Result struct {
			Turn mainNoticeTurn `json:"turn"`
		} `json:"result"`
	}
	if handledBy != owner || json.Unmarshal(raw, &response) != nil || response.Result.Turn.ID == "" || baseline[response.Result.Turn.ID] || response.Result.Turn.Status != "inProgress" && response.Result.Turn.Status != "completed" {
		return Result{}, errors.New("main_notice_turn_unconfirmed")
	}
	return reader.waitCodexHiddenTurn(r.Target.SessionID, response.Result.Turn.ID, allowSilent)
}

func (p *mainNoticeRPC) waitMainNoticeTurn(session, id string) (Result, error) {
	return p.waitCodexHiddenTurn(session, id, false)
}

func (p *mainNoticeRPC) waitCodexHiddenTurn(session, id string, allowSilent bool) (Result, error) {
	for {
		// Read only the newest receipt; unrelated historical turns can contain
		// large tool bodies. A displaced target is never inferred or replayed.
		turns, err := p.mainNoticeTurns(session, 1, "full")
		if err != nil {
			return Result{}, err
		}
		for _, turn := range turns {
			if turn.ID != id {
				continue
			}
			if len(turn.Error) > 0 && string(turn.Error) != "null" || turn.Status == "failed" {
				return Result{}, errors.New("main_notice_completion_unconfirmed")
			}
			// A read-only server can temporarily expose an unfinished on-disk
			// turn as interrupted. Only completed plus its own final is success.
			if turn.Status != "completed" {
				continue
			}
			if allowSilent && turn.Items == nil {
				return Result{}, errors.New("codex_pm_items_unconfirmed")
			}
			text := ""
			for _, item := range turn.Items {
				if item.Type == "userMessage" {
					return Result{}, errors.New("main_notice_visible_input_unexpected")
				}
				if item.Type == "agentMessage" && item.Phase == "final_answer" {
					text = item.Text
				}
				if allowSilent && item.Type == "agentMessage" && item.Phase != "final_answer" && strings.TrimSpace(item.Text) != "" {
					return Result{}, errors.New("codex_pm_unexpected_commentary")
				}
			}
			if strings.TrimSpace(text) == "" && !allowSilent {
				return Result{}, errors.New("main_notice_completion_unconfirmed")
			}
			return Result{Text: text, SessionID: session, RunID: id}, nil
		}
		tick := time.NewTimer(time.Second)
		select {
		case <-p.ctx.Done():
			tick.Stop()
			return Result{}, p.ctx.Err()
		case <-tick.C:
		}
	}
}
