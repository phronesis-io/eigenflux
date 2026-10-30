package dispatch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"
)

// Queue through the host that owns the thread; another exec/resume process
// cannot acquire a desktop-owned writer. Read receipts from the host's store,
// without copying history into an EigenFlux conversation store.
func (r Runner) runCodexQueue(ctx context.Context, req Request) (Result, error) {
	path, err := r.Binding.mainSessionPath()
	if err != nil {
		return Result{}, ErrMainSessionUnavailable
	}
	f, err := os.Open(path)
	if err != nil {
		return Result{}, ErrMainSessionUnavailable
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return Result{}, ErrMainSessionUnavailable
	}
	offset := before.Size()
	// An active turn may be in the middle of appending an unrelated record.
	skipLeading := false
	if offset > 0 {
		var last [1]byte
		if _, err = f.ReadAt(last[:], offset-1); err != nil {
			return Result{}, ErrMainSessionUnavailable
		}
		skipLeading = last[0] != '\n'
	}
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return Result{}, ErrMainSessionUnavailable
	}
	args := append(append([]string{}, r.Binding.Args...), "queue", "--thread", req.SessionID, "--message", req.Prompt)
	if _, _, err = r.runProcess(ctx, args, ""); err != nil {
		return Result{}, err
	}
	// Queue exit status proves admission only. Require our exact input, its
	// thread/turn, a final Agent message, and completion of that same turn.
	receipt := codexQueueReceipt{request: req}
	reader := bufio.NewReader(f)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var partial []byte
	for {
		if ctx.Err() != nil {
			return receipt.result(), ctx.Err()
		}
		fragment, readErr := reader.ReadSlice('\n')
		if !skipLeading {
			if len(partial)+len(fragment) > maxRunnerOutput {
				return receipt.result(), errOutputLimit
			}
			partial = append(partial, fragment...)
		}
		if readErr == nil {
			if skipLeading {
				skipLeading = false
				continue
			}
			done, err := receipt.consume(partial)
			partial = partial[:0]
			if err != nil || done {
				return receipt.result(), err
			}
			continue
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if !errors.Is(readErr, io.EOF) {
			return receipt.result(), errors.New("codex_queue_receipt_unavailable")
		}
		current, statErr := os.Stat(path)
		position, seekErr := f.Seek(0, io.SeekCurrent)
		if statErr != nil || seekErr != nil || !os.SameFile(before, current) || current.Size() < position {
			return receipt.result(), errors.New("codex_queue_store_changed")
		}
		select {
		case <-ctx.Done():
			return receipt.result(), ctx.Err()
		case <-ticker.C:
		}
	}
}

type codexQueueReceipt struct {
	request Request
	turnID  string
	text    string
}

func (r *codexQueueReceipt) result() Result {
	if r.turnID == "" {
		return Result{}
	}
	return Result{SessionID: r.request.SessionID, RunID: r.turnID, Text: r.text}
}

func (r *codexQueueReceipt) consume(line []byte) (bool, error) {
	var event struct {
		Type    string `json:"type"`
		Payload struct {
			Type             string `json:"type"`
			ThreadID         string `json:"thread_id"`
			TurnID           string `json:"turn_id"`
			LastAgentMessage string `json:"last_agent_message"`
			Item             struct {
				Type    string `json:"type"`
				Phase   string `json:"phase"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"item"`
		} `json:"payload"`
	}
	if len(line) > maxRunnerOutput || !json.Valid(bytes.TrimSpace(line)) || json.Unmarshal(line, &event) != nil {
		return false, errors.New("invalid_codex_queue_receipt")
	}
	if event.Type != "event_msg" {
		return false, nil
	}
	p := event.Payload
	var text strings.Builder
	for _, content := range p.Item.Content {
		text.WriteString(content.Text)
	}
	if p.Type == "item_completed" && p.Item.Type == "UserMessage" && text.String() == r.request.Prompt {
		if p.ThreadID != r.request.SessionID {
			return false, ErrMainSessionMismatch
		}
		if p.TurnID == "" || r.turnID != "" && r.turnID != p.TurnID {
			return false, errors.New("ambiguous_codex_queue_turn")
		}
		r.turnID = p.TurnID
	}
	if r.turnID == "" || p.TurnID != r.turnID {
		return false, nil
	}
	switch p.Type {
	case "item_completed":
		if p.ThreadID != r.request.SessionID {
			return false, ErrMainSessionMismatch
		}
		if p.Item.Type == "AgentMessage" && p.Item.Phase == "final_answer" {
			r.text = text.String()
		}
	case "task_complete":
		if strings.TrimSpace(r.text) == "" || p.LastAgentMessage != r.text {
			return false, errors.New("codex_queue_completion_unconfirmed")
		}
		return true, nil
	case "turn_aborted", "task_failed":
		return false, errors.New("codex_queue_turn_failed")
	}
	return false, nil
}
