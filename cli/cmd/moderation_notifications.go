package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"cli.eigenflux.ai/internal/desktopnotify"
)

const moderationNotificationSource = "commission_moderation"

type moderationNotification struct {
	ID      notificationID `json:"notification_id"`
	Source  string         `json:"source_type"`
	Type    string         `json:"type"`
	Payload struct {
		ModerationID        notificationID `json:"moderation_id"`
		CommissionID        notificationID `json:"commission_id"`
		RecipientAgentID    notificationID `json:"recipient_agent_id"`
		DraftVersion        int64          `json:"draft_version"`
		NotificationVersion int64          `json:"notification_version"`
		Status              string         `json:"status"`
		Decision            string         `json:"decision"`
		Flag                string         `json:"flag"`
		Field               string         `json:"field"`
		Reason              string         `json:"reason"`
		OccurredAt          int64          `json:"occurred_at"`
	} `json:"payload"`
}

func parseModerationNotification(raw json.RawMessage, agentID string) (moderationNotification, error) {
	var notice moderationNotification
	if json.Unmarshal(raw, &notice) != nil || notice.ID <= 0 || notice.Source != moderationNotificationSource || notice.Type != "commission.moderation.changed.v1" || notice.Payload.ModerationID <= 0 || notice.Payload.CommissionID <= 0 || notice.Payload.DraftVersion <= 0 || notice.Payload.NotificationVersion <= 0 || notice.Payload.OccurredAt <= 0 || strconv.FormatInt(int64(notice.Payload.RecipientAgentID), 10) != agentID {
		return notice, errors.New("invalid_moderation_notification")
	}
	if moderationStatusText("en", notice.Payload.Status) == "" {
		return notice, errors.New("invalid_moderation_notification_status")
	}
	return notice, nil
}

func moderationStatusText(language, status string) string {
	labels := map[string][2]string{
		"pending":   {"Queued for review", "已提交审核"},
		"review":    {"Awaiting manual review", "等待人工审核"},
		"reject":    {"Listing rejected", "审核未通过"},
		"pass":      {"Approved; publication pending", "人工审核通过，等待上架"},
		"published": {"Service published", "服务已上架"},
		"failed":    {"Review or publication failed", "审核或上架失败"},
		"stale":     {"Listing changed; publish again", "服务内容已变更，请重新提交上架"},
	}
	label := labels[status]
	if language == "zh" {
		return label[1]
	}
	return label[0]
}

func moderationNotificationText(language string, notice moderationNotification) (string, string) {
	title := fmt.Sprintf("Service #%d · %s", notice.Payload.CommissionID, moderationStatusText(language, notice.Payload.Status))
	body := fmt.Sprintf("Draft v%d", notice.Payload.DraftVersion)
	if language == "zh" {
		title = fmt.Sprintf("服务 #%d · %s", notice.Payload.CommissionID, moderationStatusText(language, notice.Payload.Status))
		body = fmt.Sprintf("草稿版本 %d", notice.Payload.DraftVersion)
	}
	if notice.Payload.Field != "" {
		body += " · " + notice.Payload.Field
	}
	if notice.Payload.Reason != "" {
		body += "\n" + notice.Payload.Reason
	}
	// Provider guidance is display data; remove controls and bound OS notification text.
	body = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' {
			return -1
		}
		return r
	}, body)
	if runes := []rune(body); len(runes) > 1900 {
		body = string(runes[:1900]) + "…"
	}
	return title, body
}

func (w *accountWatch) enqueueModerationNotification(raw json.RawMessage) (bool, error) {
	notice, err := parseModerationNotification(raw, w.identity.AgentID)
	if err != nil {
		return false, err
	}
	// Without a durable delivery sink, retain the server notice for later delivery.
	if w.desktop == nil {
		return false, nil
	}
	target, err := url.Parse(w.server.Endpoint)
	if err != nil || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
		return false, errors.New("invalid_moderation_notification_origin")
	}
	target.Path, target.RawPath = "/dashboard/trade", ""
	title, body := moderationNotificationText(desktopnotify.SystemLanguage(), notice)
	message := desktopnotify.Message{ID: w.scope + ":moderation:" + strconv.FormatInt(int64(notice.ID), 10), Title: title, Body: body, URL: target.String()}
	if err := w.desktop.Enqueue(message); err != nil {
		return false, err
	}
	return true, nil
}

func renderModerationNotification(writer io.Writer, format, language string, notice moderationNotification) error {
	if format == "json" {
		return json.NewEncoder(writer).Encode(map[string]any{"type": "commission_moderation_notification", "notification": notice})
	}
	title, body := moderationNotificationText(language, notice)
	_, err := fmt.Fprintf(writer, "%s\n%s\n", title, body)
	return err
}
