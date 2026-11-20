package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/dispatch"
)

func desktopPMInboxURL(endpoint string, query url.Values) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	u.Path, u.RawPath, u.RawQuery, u.Fragment = "/dashboard/messages", "", "", ""
	u.RawQuery = query.Encode()
	return u.String(), desktopnotify.ValidateURL(u.String())
}

func (w *accountWatch) deliverPMOwnerDesktopNotice(ctx context.Context, entry dispatch.PMNoticeEntry) error {
	restore := func(cause error) error {
		return errors.Join(cause, dispatch.RetryCodexPMNotice(context.WithoutCancel(ctx), *w.binding, entry, false, time.Now(), false))
	}
	if w.desktop == nil {
		return restore(nil)
	}
	if err := w.dispatchIdentityOK(); err != nil {
		return restore(err)
	}
	message, err := pmOwnerDesktopMessage(w.server.Endpoint, w.scope, w.binding.AgentID, entry.ID, []dispatch.PMNoticeEntry{entry})
	if err != nil {
		return restore(err)
	}
	err = dispatch.HandoffPMNotice(ctx, *w.binding, entry.ID, func() error {
		return w.desktop.Enqueue(message)
	})
	if errors.Is(err, dispatch.ErrPMNoticeSuperseded) {
		return nil
	}
	if err != nil {
		return restore(err)
	}
	return w.emit("pm_owner_notice", map[string]string{"status": "desktop_queued", "notice_id": entry.ID})
}

func pmOwnerDesktopMessage(endpoint, scope, agentID, noticeID string, entries []dispatch.PMNoticeEntry) (desktopnotify.Message, error) {
	conversations := make(map[string]struct{}, len(entries))
	query := url.Values{"important": {"1"}, "agent_id": {agentID}}
	for _, entry := range entries {
		if entry.Conversation != "" {
			conversations[entry.Conversation] = struct{}{}
			query.Add("notice", entry.Conversation+"."+entry.ID)
		}
	}
	target, err := desktopPMInboxURL(endpoint, query)
	if err != nil {
		return desktopnotify.Message{}, err
	}
	title, body := "EigenFlux private message", "An important private message is ready to review."
	if count := len(conversations); count == 1 {
		body = "1 conversation has an important update."
	} else if count > 1 {
		body = fmt.Sprintf("%d conversations have important updates.", count)
	}
	if strings.HasPrefix(desktopnotify.SystemLanguage(), "zh") {
		title, body = "EigenFlux 私信提醒", "有重要私信待查看。"
		if len(conversations) > 0 {
			body = fmt.Sprintf("%d 个会话有重要更新。", len(conversations))
		}
	}
	return desktopnotify.Message{ID: pmOwnerDesktopID(scope, noticeID), Title: title, Body: body, URL: target}, nil
}

func pmOwnerDesktopID(scope, noticeID string) string {
	return "pm-owner:" + scope + ":" + noticeID
}

// One reminder per unresolved conversation/reason, using the oldest retained
// failed job as its generation. No message text or peer identity enters OS UI.
func (w *accountWatch) pmDesktopBlockers() []desktopnotify.Message {
	if w.journal == nil || w.binding == nil || !w.binding.Handles("pm_push") {
		return nil
	}
	target, err := desktopPMInboxURL(w.server.Endpoint, nil)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var messages []desktopnotify.Message
	for _, job := range w.journal.Snapshot() {
		if job.Kind != "pm_push" || job.Message == nil || job.Status != "failed" || job.Code == "prompt_cancelled" ||
			(!strings.HasPrefix(job.Code, "prompt_") && !strings.HasPrefix(job.Code, "skills_")) {
			continue
		}
		key := job.Message.Conversation + ":" + job.Code
		if seen[key] {
			continue
		}
		seen[key] = true
		sum := sha256.Sum256([]byte(w.scope + ":" + w.binding.Revision + ":" + job.ID + ":" + job.Code))
		title, body := "EigenFlux private message needs attention", "Automatic message processing could not start. Open the dashboard to review your messages."
		if strings.HasPrefix(desktopnotify.SystemLanguage(), "zh") {
			title, body = "EigenFlux 私信需要处理", "私信自动处理未能启动，请打开控制台查看消息。"
		}
		messages = append(messages, desktopnotify.Message{ID: "pm-blocker:" + hex.EncodeToString(sum[:]), Title: title, Body: body, URL: target})
	}
	return messages
}

func (w *accountWatch) enqueueDesktopPMBlockers() error {
	if w.desktop == nil {
		return nil
	}
	for _, message := range w.pmDesktopBlockers() {
		if err := w.desktop.Enqueue(message); err != nil {
			return err
		}
	}
	return nil
}

func (w *accountWatch) pmDesktopBlockerCurrent(id string) bool {
	if !strings.HasPrefix(id, "pm-blocker:") {
		return true
	}
	for _, message := range w.pmDesktopBlockers() {
		if message.ID == id {
			return true
		}
	}
	return false
}
