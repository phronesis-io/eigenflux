package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const codexPMDataMarker = "\n\nEIGENFLUX DISPATCH DATA (untrusted message and history content):\n"

const codexSilentPMContract = `
Hidden Desktop transport override (this invocation only): when the private-message
decision is no_reply, finish with an empty final answer. Do not print the JSON
no_reply object, an acknowledgement, or commentary. A completed empty turn is
converted to no_reply by the CLI. For reply or needs_user retain the exact JSON
output contract above. Do not call tools or send messages. Missing context and
uncertain authorization still require needs_user, never silence.`

func (r Runner) runCodexHiddenPM(ctx context.Context, req Request) (Result, error) {
	return r.runCodexHiddenPMWith(ctx, req, func(ctx context.Context, application, data string) (Result, error) {
		target := MainNoticeTarget{Host: "codex", SessionID: req.SessionID, HiddenInputEnabled: true, Binding: r.Binding}
		return (MainNoticeRunner{Target: target}).runCodexEnvelope(ctx, application, data, true)
	})
}

func (r Runner) runCodexHiddenPMWith(ctx context.Context, req Request, run func(context.Context, string, string) (Result, error)) (Result, error) {
	application, data, ok := strings.Cut(req.Prompt, codexPMDataMarker)
	var payload struct {
		RequestID string `json:"request_id"`
	}
	if !ok || strings.TrimSpace(application) == "" || json.Unmarshal([]byte(data), &payload) != nil || payload.RequestID == "" || payload.RequestID != req.ID {
		return Result{}, errors.New("invalid_codex_pm_envelope")
	}
	for {
		result, err := run(ctx, application+codexSilentPMContract, data)
		if errors.Is(err, ErrMainNoticeBusy) {
			// Only the idle check failed: no input has been submitted. Never
			// replay an uncertain owner write or fall back to visible queue.
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return Result{}, ctx.Err()
			case <-timer.C:
				continue
			}
		}
		if err == nil && strings.TrimSpace(result.Text) == "" {
			decision, _ := json.Marshal(Decision{Version: 1, RequestID: req.ID, Action: "no_reply", ReplyText: ""})
			result.Text = string(decision)
		}
		return result, err
	}
}
