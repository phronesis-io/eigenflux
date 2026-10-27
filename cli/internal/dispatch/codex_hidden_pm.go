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
## Windows Desktop Output

This invocation uses a content-only Desktop transport. The CLI owns all JSON
protocol fields and correlates the result with the exact admitted thread/turn.
- reply: the final answer is ONLY the reply body, with no JSON envelope, action,
  request identifier, acknowledgement, commentary or extra explanation.
- no_reply: finish with an empty final answer. Print nothing.
- needs_user: output one owner-facing explanation beginning exactly with
  "EigenFlux 需要你确认：" followed by the concrete question or blocker. The CLI
  holds this decision for the owner and NEVER sends this text to the peer.
Do not call tools or send messages. Missing context and uncertain authorization
still require needs_user, never a reply or silence.`

const codexPMNeedsUserPrefix = "EigenFlux 需要你确认："

// Replace only the synchronized output section. Keep all authority rules and
// any later sections; conflicting visible-JSON instructions must not reach UI.
func codexContentPMRules(application string) string {
	if before, after, ok := strings.Cut(application, "\n## Output Contract"); ok {
		application = before
		if next := strings.Index(after, "\n## "); next >= 0 {
			application += after[next:]
		}
	}
	return application + codexSilentPMContract
}

func codexPMContentDecision(text, requestID string) (string, error) {
	body := strings.TrimSpace(text)
	d := Decision{Version: 1, RequestID: requestID, Action: "reply", ReplyText: body}
	switch {
	case body == "":
		d.Action, d.ReplyText = "no_reply", ""
	case strings.HasPrefix(body, codexPMNeedsUserPrefix):
		if strings.TrimSpace(strings.TrimPrefix(body, codexPMNeedsUserPrefix)) == "" {
			return "", errors.New("codex_pm_empty_owner_question")
		}
		d.Action, d.ReplyText = "needs_user", ""
	default:
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(body), &fields) == nil && fields["request_id"] != nil && fields["action"] != nil {
			return "", errors.New("codex_pm_visible_protocol_result")
		}
	}
	raw, err := json.Marshal(d)
	return string(raw), err
}

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
		result, err := run(ctx, codexContentPMRules(application), data)
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
		if err == nil {
			result.Text, err = codexPMContentDecision(result.Text, req.ID)
		}
		return result, err
	}
}
