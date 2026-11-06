package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"

	"cli.eigenflux.ai/internal/desktopnotify"
)

// One reminder per unresolved conversation/reason, using the oldest retained
// failed job as its generation. No message text or peer identity enters OS UI.
func (w *accountWatch) pmDesktopBlockers() []desktopnotify.Message {
	if w.journal == nil || w.binding == nil || !w.binding.Handles("pm_push") {
		return nil
	}
	u, err := url.Parse(w.server.Endpoint)
	if err != nil {
		return nil
	}
	u.Path, u.RawPath, u.RawQuery, u.Fragment = "/dashboard", "", "", ""
	if desktopnotify.ValidateURL(u.String()) != nil {
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
		messages = append(messages, desktopnotify.Message{ID: "pm-blocker:" + hex.EncodeToString(sum[:]), Title: title, Body: body, URL: u.String()})
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
